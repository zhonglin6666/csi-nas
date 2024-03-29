/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package nas

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-csi/drivers/pkg/csi-common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/zhonglin6666/csi-nas/pkg/utils"
)

type nodeServer struct {
	*csicommon.DefaultNodeServer
}

type NasOptions struct {
	Server  string `json:"server"`
	Path    string `json:"path"`
	Vers    string `json:"vers"`
	Mode    string `json:"mode"`
	Options string `json:"options"`
}

const (
	NAS_TEMP_MNTPath = "/mnt/acs_mnt/k8s_nas/temp" // used for create sub directory;
	NAS_PORTNUM      = "2049"
)

func (ns *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	log.Infof("NodePublishVolume:: Nas Mount with: %s", req.VolumeContext)

	// parse parameters
	mountPath := req.GetTargetPath()
	opt, err := getRequestParameters(req)
	if err != nil {
		return nil, err
	}

	if utils.IsMounted(mountPath) {
		log.Infof("Nas, Mount Path Already Mount, options: %s", mountPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	// if system not set nas, config it.
	checkSystemNasConfig()

	// Create Mount Path
	if err := utils.CreateDest(mountPath); err != nil {
		return nil, errors.New("Nas, Mount error with create Path fail: " + mountPath)
	}

	// Do mount
	mntCmd := fmt.Sprintf("mount -t nfs -o vers=%s %s:%s %s", opt.Vers, opt.Server, opt.Path, mountPath)
	if opt.Options != "" {
		mntCmd = fmt.Sprintf("mount -t nfs -o vers=%s,%s %s:%s %s", opt.Vers, opt.Options, opt.Server, opt.Path, mountPath)
	}
	_, err = utils.Run(mntCmd)

	// Mount to nfs Sub-directory
	if err != nil && opt.Path != "/" {
		if strings.Contains(err.Error(), "reason given by server: No such file or directory") || strings.Contains(err.Error(), "access denied by server while mounting") {
			ns.createNasSubDir(opt, req.VolumeId)
			if _, err := utils.Run(mntCmd); err != nil {
				log.Errorf("Nas, Mount Nfs sub directory fail: %s", err.Error())
				return nil, errors.New("Nas, Mount Nfs sub directory fail: %s" + err.Error())
			}
		} else {
			log.Errorf("Nas, Mount Nfs fail with error: %s", err.Error())
			return nil, errors.New("Nas, Mount Nfs fail with error: %s" + err.Error())
		}
		// mount error
	} else if err != nil {
		log.Errorf("Nas, Mount nfs fail: %s", err.Error())
		return nil, errors.New("Nas, Mount nfs fail: %s" + err.Error())
	}
	log.Infof("NodePublishVolume:: Exec mount command: %s", mntCmd)

	// change the mode
	if opt.Mode != "" && opt.Path != "/" {
		var wg1 sync.WaitGroup
		wg1.Add(1)

		go func(*sync.WaitGroup) {
			cmd := fmt.Sprintf("chmod -R %s %s", opt.Mode, mountPath)
			if _, err := utils.Run(cmd); err != nil {
				log.Errorf("Nas chmod cmd fail: %s %s", cmd, err)
			} else {
				log.Infof("Nas chmod cmd success: %s", cmd)
			}
			wg1.Done()
		}(&wg1)

		if waitTimeout(&wg1, 1) {
			log.Infof("Chmod use more than 1s, running in Concurrency: %s", mountPath)
		}
	}

	// check mount
	if !utils.IsMounted(mountPath) {
		return nil, errors.New("Check mount fail after mount:" + mountPath)
	}
	log.Infof("NodePublishVolume:: Mount success on mountpoint: %s", mountPath)

	return &csi.NodePublishVolumeResponse{}, nil
}

// parse parameters and checkout parameters
func getRequestParameters(req *csi.NodePublishVolumeRequest) (*NasOptions, error) {
	mountPath := req.GetTargetPath()
	opt := &NasOptions{}
	for key, value := range req.VolumeContext {
		if key == "server" {
			opt.Server = value
		} else if key == "path" {
			opt.Path = value
		} else if key == "vers" {
			opt.Vers = value
		} else if key == "mode" {
			opt.Mode = value
		} else if key == "options" {
			opt.Options = value
		}
	}

	// check parameters
	if mountPath == "" {
		return nil, errors.New("mountPath is empty")
	}
	if opt.Server == "" {
		return nil, errors.New("server is empty")
	}
	// check network connection
	conn, err := net.DialTimeout("tcp", opt.Server+":"+NAS_PORTNUM, time.Second*time.Duration(3))
	if err != nil {
		log.Errorf("Cannot connect to nas host: %s", opt.Server)
		return nil, errors.New("Cannot connect to nas host: " + opt.Server)
	}
	defer conn.Close()

	if opt.Path == "" {
		opt.Path = "/"
	}
	if opt.Path == "/" && opt.Mode != "" {
		return nil, errors.New("root directory cannot set mode: " + opt.Mode)
	}
	if !strings.HasPrefix(opt.Path, "/") {
		return nil, errors.New("the path format is illegal")
	}

	switch opt.Vers {
	case "", "3.0":
		opt.Vers = "3"
	case "4":
		opt.Vers = "4.0"
	default:
		return nil, errors.New("nas version invalid")
	}

	// check options
	if opt.Options == "" {
		if opt.Vers == "3" {
			opt.Options = "noresvport,nolock,tcp"
		} else {
			opt.Options = "noresvport"
		}
	} else if strings.ToLower(opt.Options) == "none" {
		opt.Options = ""
	}

	return opt, nil
}

// check system config,
// if tcp_slot_table_entries not set to 128, just config.
func checkSystemNasConfig() {
	updateNasConfig := false
	sunRpcFile := "/etc/modprobe.d/sunrpc.conf"
	if !utils.IsFileExisting(sunRpcFile) {
		updateNasConfig = true
	} else {
		chkCmd := fmt.Sprintf("cat %s | grep tcp_slot_table_entries | grep 128 | grep -v grep | wc -l", sunRpcFile)
		out, err := utils.Run(chkCmd)
		if err != nil {
			log.Warnf("Update Nas system config check error: %s", err.Error())
			return
		}
		if strings.TrimSpace(out) == "0" {
			updateNasConfig = true
		}
	}

	if updateNasConfig {
		upCmd := fmt.Sprintf("echo \"options sunrpc tcp_slot_table_entries=128\" >> %s && echo \"options sunrpc tcp_max_slot_table_entries=128\" >> %s && sysctl -w sunrpc.tcp_slot_table_entries=128", sunRpcFile, sunRpcFile)
		_, err := utils.Run(upCmd)
		if err != nil {
			log.Warnf("Update Nas system config error: %s", err.Error())
			return
		}
		log.Warnf("Successful update Nas system config")
	}
}

func waitTimeout(wg *sync.WaitGroup, timeout int) bool {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		return false
	case <-time.After(time.Duration(timeout) * time.Second):
		return true
	}

}

func (ns *nodeServer) createNasSubDir(opt *NasOptions, volumeId string) {
	// step 1: create mount path
	nasTmpPath := filepath.Join(NAS_TEMP_MNTPath, volumeId)
	if err := utils.CreateDest(nasTmpPath); err != nil {
		log.Infof("Create Nas temp Directory err: " + err.Error())
		return
	}
	if utils.IsMounted(nasTmpPath) {
		utils.Umount(nasTmpPath)
	}

	// step 2: do mount
	usePath := opt.Path
	mntCmd := fmt.Sprintf("mount -t nfs -o vers=%s %s:%s %s", opt.Vers, opt.Server, "/", nasTmpPath)
	if opt.Options != "" {
		mntCmd = fmt.Sprintf("mount -t nfs -o vers=%s,%s %s:%s %s", opt.Vers, opt.Options, opt.Server, "/", nasTmpPath)
	}
	_, err := utils.Run(mntCmd)
	if err != nil {
		if strings.Contains(err.Error(), "reason given by server: No such file or directory") || strings.Contains(err.Error(), "access denied by server while mounting") {
			if strings.HasPrefix(opt.Path, "/share/") {
				usePath = usePath[6:]
				mntCmd = fmt.Sprintf("mount -t nfs -o vers=%s %s:%s %s", opt.Vers, opt.Server, "/share", nasTmpPath)
				if opt.Options != "" {
					mntCmd = fmt.Sprintf("mount -t nfs -o vers=%s,%s %s:%s %s", opt.Vers, opt.Options, opt.Server, "/share", nasTmpPath)
				}
				_, err := utils.Run(mntCmd)
				if err != nil {
					log.Errorf("Nas, Mount to temp directory(with /share) fail: %s", err.Error())
					return
				}
			} else {
				log.Errorf("Nas, maybe use fast nas, but path not startwith /share: %s", err.Error())
				return
			}
		} else {
			log.Errorf("Nas, Mount to temp directory fail: %s", err.Error())
			return
		}
	}
	subPath := path.Join(nasTmpPath, usePath)
	if err := utils.CreateDest(subPath); err != nil {
		log.Infof("Nas, Create Sub Directory err: " + err.Error())
		return
	}

	// step 3: umount after create
	utils.Umount(nasTmpPath)
	log.Infof("Create Sub Directory successful: %s", opt.Path)
}

func (ns *nodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	log.Infof("NodeUnpublishVolume:: Starting Umount Nas: %s", req.TargetPath)
	mountPoint := req.TargetPath
	if !utils.IsMounted(mountPoint) {
		return &csi.NodeUnpublishVolumeResponse{}, nil
	}

	umntCmd := fmt.Sprintf("umount %s", mountPoint)
	if _, err := utils.Run(umntCmd); err != nil {
		return nil, errors.New("Nas, Umount nfs Fail: " + err.Error())
	}

	log.Infof("Umount Nas Successful on: %s", mountPoint)
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (ns *nodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (ns *nodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (ns *nodeServer) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

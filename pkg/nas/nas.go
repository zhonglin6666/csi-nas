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
	log "github.com/Sirupsen/logrus"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-csi/drivers/pkg/csi-common"
)

const (
	PluginFolder = "/var/lib/kubelet/plugins/csi.nasplugin.com"
	driverName   = "csi.nasplugin.com"
	INSTANCE_ID  = "instance-id"
)

var (
	version = "1.0.0"
)

type nas struct {
	driver   *csicommon.CSIDriver
	endpoint string

	ids *csicommon.DefaultIdentityServer
	ns  *nodeServer
	cs  *ControllerServer

	cap   []*csi.VolumeCapability_AccessMode
	cscap []*csi.ControllerServiceCapability
}

func NewDriver(nodeID, endpoint string) *nas {
	log.Infof("Driver: %v version: %v", driverName, version)

	d := &nas{endpoint: endpoint}

	csiDriver := csicommon.NewCSIDriver(driverName, version, nodeID)
	csiDriver.AddVolumeCapabilityAccessModes([]csi.VolumeCapability_AccessMode_Mode{
		csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
	})
	csiDriver.AddControllerServiceCapabilities([]csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
		csi.ControllerServiceCapability_RPC_LIST_VOLUMES,
	})

	d.driver = csiDriver

	return d
}

func NewNodeServer(d *nas) *nodeServer {
	return &nodeServer{
		DefaultNodeServer: csicommon.NewDefaultNodeServer(d.driver),
	}
}

func (d *nas) Run() {
	s := csicommon.NewNonBlockingGRPCServer()

	d.ids = csicommon.NewDefaultIdentityServer(d.driver)
	d.ns = NewNodeServer(d)

	// TODO
	d.cs = NewControllerServer(d.driver, "", "")

	s.Start(d.endpoint, d.ids, d.cs, d.ns)
	s.Wait()
}

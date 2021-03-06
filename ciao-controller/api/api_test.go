// Copyright (c) 2016 Intel Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ciao-project/ciao/ciao-controller/types"
	storage "github.com/ciao-project/ciao/ciao-storage"
	"github.com/ciao-project/ciao/payloads"
	"github.com/ciao-project/ciao/service"
)

type test struct {
	method           string
	request          string
	requestBody      string
	media            string
	expectedStatus   int
	expectedResponse string
}

var tests = []test{
	{
		"GET",
		"/",
		"",
		"application/text",
		http.StatusOK,
		`[{"rel":"pools","href":"/pools","version":"x.ciao.pools.v1","minimum_version":"x.ciao.pools.v1"},{"rel":"external-ips","href":"/external-ips","version":"x.ciao.external-ips.v1","minimum_version":"x.ciao.external-ips.v1"},{"rel":"workloads","href":"/workloads","version":"x.ciao.workloads.v1","minimum_version":"x.ciao.workloads.v1"},{"rel":"tenants","href":"/tenants","version":"x.ciao.tenants.v1","minimum_version":"x.ciao.tenants.v1"},{"rel":"node","href":"/node","version":"x.ciao.node.v1","minimum_version":"x.ciao.node.v1"},{"rel":"images","href":"/images","version":"x.ciao.images.v1","minimum_version":"x.ciao.images.v1"}]`,
	},
	{
		"GET",
		"/pools",
		"",
		fmt.Sprintf("application/%s", PoolsV1),
		http.StatusOK,
		`{"pools":[{"id":"ba58f471-0735-4773-9550-188e2d012941","name":"testpool","free":0,"total_ips":0,"links":[{"rel":"self","href":"/pools/ba58f471-0735-4773-9550-188e2d012941"}]}]}`,
	},
	{
		"GET",
		"/pools?name=testpool",
		"",
		fmt.Sprintf("application/%s", PoolsV1),
		http.StatusOK,
		`{"pools":[{"id":"ba58f471-0735-4773-9550-188e2d012941","name":"testpool","free":0,"total_ips":0,"links":[{"rel":"self","href":"/pools/ba58f471-0735-4773-9550-188e2d012941"}]}]}`,
	},
	{
		"POST",
		"/pools",
		`{"name":"testpool"}`,
		fmt.Sprintf("application/%s", PoolsV1),
		http.StatusNoContent,
		"null",
	},
	{
		"GET",
		"/pools/ba58f471-0735-4773-9550-188e2d012941",
		"",
		fmt.Sprintf("application/%s", PoolsV1),
		http.StatusOK,
		`{"id":"ba58f471-0735-4773-9550-188e2d012941","name":"testpool","free":0,"total_ips":0,"links":[{"rel":"self","href":"/pools/ba58f471-0735-4773-9550-188e2d012941"}],"subnets":[],"ips":[]}`,
	},
	{
		"DELETE",
		"/pools/ba58f471-0735-4773-9550-188e2d012941",
		"",
		fmt.Sprintf("application/%s", PoolsV1),
		http.StatusNoContent,
		"null",
	},
	{
		"POST",
		"/pools/ba58f471-0735-4773-9550-188e2d012941",
		`{"subnet":"192.168.0.0/24"}`,
		fmt.Sprintf("application/%s", PoolsV1),
		http.StatusNoContent,
		"null",
	},
	{
		"DELETE",
		"/pools/ba58f471-0735-4773-9550-188e2d012941/subnets/ba58f471-0735-4773-9550-188e2d012941",
		"",
		fmt.Sprintf("application/%s", PoolsV1),
		http.StatusNoContent,
		"null",
	},
	{
		"DELETE",
		"/pools/ba58f471-0735-4773-9550-188e2d012941/external-ips/ba58f471-0735-4773-9550-188e2d012941",
		"",
		fmt.Sprintf("application/%s", PoolsV1),
		http.StatusNoContent,
		"null",
	},
	{
		"GET",
		"/external-ips",
		"",
		fmt.Sprintf("application/%s", ExternalIPsV1),
		http.StatusOK,
		`[{"mapping_id":"ba58f471-0735-4773-9550-188e2d012941","external_ip":"192.168.0.1","internal_ip":"172.16.0.1","instance_id":"","tenant_id":"8a497c68-a88a-4c1c-be56-12a4883208d3","pool_id":"f384ffd8-e7bd-40c2-8552-2efbe7e3ad6e","pool_name":"mypool","links":[{"rel":"self","href":"/external-ips/ba58f471-0735-4773-9550-188e2d012941"},{"rel":"pool","href":"/pools/f384ffd8-e7bd-40c2-8552-2efbe7e3ad6e"}]}]`,
	},
	{
		"POST",
		"/19df9b86-eda3-489d-b75f-d38710e210cb/external-ips",
		`{"pool_name":"apool","instance_id":"validinstanceID"}`,
		fmt.Sprintf("application/%s", ExternalIPsV1),
		http.StatusNoContent,
		"null",
	},
	{
		"POST",
		"/workloads",
		`{"id":"","description":"testWorkload","fw_type":"legacy","vm_type":"qemu","image_name":"","config":"this will totally work!"}`,
		fmt.Sprintf("application/%s", WorkloadsV1),
		http.StatusCreated,
		`{"workload":{"id":"ba58f471-0735-4773-9550-188e2d012941","description":"testWorkload","fw_type":"legacy","vm_type":"qemu","image_name":"","config":"this will totally work!","storage":null,"visibility":"public","workload_requirements":{"MemMB":0,"VCPUs":0,"NodeID":"","Hostname":"","NetworkNode":false,"Privileged":false}},"link":{"rel":"self","href":"/workloads/ba58f471-0735-4773-9550-188e2d012941"}}`,
	},
	{
		"DELETE",
		"/workloads/76f4fa99-e533-4cbd-ab36-f6c0f51292ed",
		"",
		fmt.Sprintf("application/%s", WorkloadsV1),
		http.StatusNoContent,
		"null",
	},
	{
		"GET",
		"/workloads/ba58f471-0735-4773-9550-188e2d012941",
		"",
		fmt.Sprintf("application/%s", WorkloadsV1),
		http.StatusOK,
		`{"id":"ba58f471-0735-4773-9550-188e2d012941","description":"testWorkload","fw_type":"legacy","vm_type":"qemu","image_name":"","config":"this will totally work!","storage":null,"visibility":"private","workload_requirements":{"MemMB":0,"VCPUs":0,"NodeID":"","Hostname":"","NetworkNode":false,"Privileged":false}}`,
	},
	{
		"GET",
		"/workloads",
		"",
		fmt.Sprintf("application/%s", WorkloadsV1),
		http.StatusOK,
		`[{"id":"ba58f471-0735-4773-9550-188e2d012941","description":"testWorkload","fw_type":"legacy","vm_type":"qemu","image_name":"","config":"this will totally work!","storage":null,"visibility":"private","workload_requirements":{"MemMB":0,"VCPUs":0,"NodeID":"","Hostname":"","NetworkNode":false,"Privileged":false}}]`,
	},
	{
		"GET",
		"/tenants/093ae09b-f653-464e-9ae6-5ae28bd03a22/quotas",
		"",
		fmt.Sprintf("application/%s", TenantsV1),
		http.StatusOK,
		`{"quotas":[{"name":"test-quota-1","value":"10","usage":"3"},{"name":"test-quota-2","value":"unlimited","usage":"10"},{"name":"test-limit","value":"123"}]}`,
	},
	{
		"GET",
		"/tenants",
		"",
		fmt.Sprintf("application/%s", TenantsV1),
		http.StatusOK,
		`{"tenants":[{"id":"bc70dcd6-7298-4933-98a9-cded2d232d02","name":"Test Tenant","links":[{"rel":"self","href":"/tenants/bc70dcd6-7298-4933-98a9-cded2d232d02"}]}]}`,
	},
	{
		"GET",
		"/tenants/093ae09b-f653-464e-9ae6-5ae28bd03a22",
		"",
		fmt.Sprintf("application/%s", TenantsV1),
		http.StatusOK,
		`{"name":"Test Tenant","subnet_bits":24,"permissions":{"privileged_containers":false}}`,
	},
	{
		"PATCH",
		"/tenants/093ae09b-f653-464e-9ae6-5ae28bd03a22",
		`{"name":"Updated Test Tenant","subnet_bits":4}`,
		fmt.Sprintf("application/%s", "merge-patch+json"),
		http.StatusNoContent,
		"null",
	},
	{
		"POST",
		"/tenants",
		`{"id":"093ae09b-f653-464e-9ae6-5ae28bd03a22","config":{"name":"New Tenant","subnet_bits":4}}`,
		fmt.Sprintf("application/%s", TenantsV1),
		http.StatusCreated,
		`{"id":"093ae09b-f653-464e-9ae6-5ae28bd03a22","name":"New Tenant","links":[{"rel":"self","href":"/tenants/093ae09b-f653-464e-9ae6-5ae28bd03a22"}]}`,
	},
	{
		"DELETE",
		"/tenants/093ae09b-f653-464e-9ae6-5ae28bd03a22",
		"",
		fmt.Sprintf("application/%s", TenantsV1),
		http.StatusNoContent,
		"null",
	}, {
		"POST",
		"/images",
		`{"container_format":"bare","disk_format":"raw","name":"Ubuntu","id":"b2173dd3-7ad6-4362-baa6-a68bce3565cb","visibility":"private"}`,
		fmt.Sprintf("application/%s", ImagesV1),
		http.StatusCreated,
		`{"id":"b2173dd3-7ad6-4362-baa6-a68bce3565cb","state":"created","tenant_id":"","name":"Ubuntu","create_time":"2015-11-29T22:21:42Z","size":0,"visibility":"private"}`,
	},
	{
		"GET",
		"/images",
		"",
		fmt.Sprintf("application/%s", ImagesV1),
		http.StatusOK,
		`[{"id":"b2173dd3-7ad6-4362-baa6-a68bce3565cb","state":"created","tenant_id":"","name":"Ubuntu","create_time":"2015-11-29T22:21:42Z","size":0,"visibility":"public"}]`,
	},
	{
		"GET",
		"/images/1bea47ed-f6a9-463b-b423-14b9cca9ad27",
		"",
		fmt.Sprintf("application/%s", ImagesV1),
		http.StatusOK,
		`{"id":"1bea47ed-f6a9-463b-b423-14b9cca9ad27","state":"active","tenant_id":"","name":"cirros-0.3.2-x86_64-disk","create_time":"2014-05-05T17:15:10Z","size":13167616,"visibility":"public"}`,
	},
	{
		"DELETE",
		"/images/1bea47ed-f6a9-463b-b423-14b9cca9ad27",
		"",
		fmt.Sprintf("application/%s", ImagesV1),
		http.StatusNoContent,
		`null`,
	},
	{
		"POST",
		"/validtenantid/volumes",
		`{"size": 10,"source_volid": null,"description":null,"name":null,"imageRef":null}`,
		fmt.Sprintf("application/%s", VolumesV1),
		http.StatusAccepted,
		`{"id":"new-test-id","bootable":false,"boot_index":0,"ephemeral":false,"local":false,"swap":false,"size":123456,"tenant_id":"test-tenant-id","state":"available","created":"0001-01-01T00:00:00Z","name":"new volume","description":"newly created volume","internal":false}`,
	},
	{
		"GET",
		"/validtenantid/volumes",
		"",
		fmt.Sprintf("application/%s", VolumesV1),
		http.StatusOK,
		`[{"id":"new-test-id","bootable":false,"boot_index":0,"ephemeral":false,"local":false,"swap":false,"size":123456,"tenant_id":"test-tenant-id","state":"available","created":"0001-01-01T00:00:00Z","name":"my volume","description":"my volume for stuff","internal":false},{"id":"new-test-id2","bootable":false,"boot_index":0,"ephemeral":false,"local":false,"swap":false,"size":123456,"tenant_id":"test-tenant-id","state":"available","created":"0001-01-01T00:00:00Z","name":"volume 2","description":"my other volume","internal":false}]`,
	},
	{
		"GET",
		"/validtenantid/volumes/validvolumeid",
		"",
		fmt.Sprintf("application/%s", VolumesV1),
		http.StatusOK,
		`{"id":"new-test-id","bootable":false,"boot_index":0,"ephemeral":false,"local":false,"swap":false,"size":123456,"tenant_id":"test-tenant-id","state":"available","created":"0001-01-01T00:00:00Z","name":"my volume","description":"my volume for stuff","internal":false}`,
	},
	{
		"DELETE",
		"/validtenantid/volumes/validvolumeid",
		"",
		fmt.Sprintf("application/%s", VolumesV1),
		http.StatusAccepted,
		"null",
	},
	{
		"POST",
		"/validtenantid/volumes/validvolumeid/action",
		`{"attach":{"instance_uuid":"validinstanceid","mountpoint":"/dev/vdc"}}`,
		fmt.Sprintf("application/%s", VolumesV1),
		http.StatusAccepted,
		"null",
	},
	{
		"POST",
		"/validtenantid/volumes/validvolumeid/action",
		`{"detach":{}}`,
		fmt.Sprintf("application/%s", VolumesV1),
		http.StatusAccepted,
		"null",
	},
	{
		"POST",
		"/validtenantid/instances",
		`{"server":{"name":"new-server-test","imageRef": "http://glance.openstack.example.com/images/70a599e0-31e7-49b7-b260-868f441e862b","workload_id":"http://openstack.example.com/flavors/1","metadata":{"My Server Name":"Apache1"}}}`,
		fmt.Sprintf("application/%s", InstancesV1),
		http.StatusAccepted,
		`{"server":{"id":"validServerID","name":"new-server-test","imageRef":"http://glance.openstack.example.com/images/70a599e0-31e7-49b7-b260-868f441e862b","workload_id":"http://openstack.example.com/flavors/1","max_count":0,"min_count":0,"metadata":{"My Server Name":"Apache1"}}}`,
	},
	{
		"GET",
		"/validtenantid/instances/detail",
		"",
		fmt.Sprintf("application/%s", InstancesV1),
		http.StatusOK,
		`{"total_servers":1,"servers":[{"private_addresses":[{"addr":"192.169.0.1","mac_addr":"00:02:00:01:02:03"}],"created":"0001-01-01T00:00:00Z","workload_id":"testWorkloadUUID","node_id":"nodeUUID","id":"testUUID","name":"","volumes":null,"status":"active","tenant_id":"validtenantid","ssh_ip":"","ssh_port":0}]}`},
	{
		"GET",
		"/validtenantid/instances/instanceid",
		"",
		fmt.Sprintf("application/%s", InstancesV1),
		http.StatusOK,
		`{"server":{"private_addresses":[{"addr":"192.169.0.1","mac_addr":"00:02:00:01:02:03"}],"created":"0001-01-01T00:00:00Z","workload_id":"testWorkloadUUID","node_id":"nodeUUID","id":"instanceid","name":"","volumes":null,"status":"active","tenant_id":"validtenantid","ssh_ip":"","ssh_port":0}}`,
	},
	{
		"DELETE",
		"/validtenantid/instances/instanceid",
		"",
		fmt.Sprintf("application/%s", InstancesV1),
		http.StatusNoContent,
		"null",
	},
	{
		"POST",
		"/validtenantid/instances/instanceid/action",
		`{"os-start":null}`,
		fmt.Sprintf("application/%s", InstancesV1),
		http.StatusAccepted,
		"null",
	},
	{
		"POST",
		"/validtenantid/instances/instanceid/action",
		`{"os-stop":null}`,
		fmt.Sprintf("application/%s", InstancesV1),
		http.StatusAccepted,
		"null",
	},
}

type testCiaoService struct{}

func (ts testCiaoService) ListPools() ([]types.Pool, error) {
	self := types.Link{
		Rel:  "self",
		Href: "/pools/ba58f471-0735-4773-9550-188e2d012941",
	}

	resp := types.Pool{
		ID:       "ba58f471-0735-4773-9550-188e2d012941",
		Name:     "testpool",
		Free:     0,
		TotalIPs: 0,
		Subnets:  []types.ExternalSubnet{},
		IPs:      []types.ExternalIP{},
		Links:    []types.Link{self},
	}

	return []types.Pool{resp}, nil
}

func (ts testCiaoService) AddPool(name string, subnet *string, ips []string) (types.Pool, error) {
	return types.Pool{}, nil
}

func (ts testCiaoService) ShowPool(id string) (types.Pool, error) {
	fmt.Println("ShowPool")
	self := types.Link{
		Rel:  "self",
		Href: "/pools/ba58f471-0735-4773-9550-188e2d012941",
	}

	resp := types.Pool{
		ID:       "ba58f471-0735-4773-9550-188e2d012941",
		Name:     "testpool",
		Free:     0,
		TotalIPs: 0,
		Subnets:  []types.ExternalSubnet{},
		IPs:      []types.ExternalIP{},
		Links:    []types.Link{self},
	}

	return resp, nil
}

func (ts testCiaoService) DeletePool(id string) error {
	return nil
}

func (ts testCiaoService) AddAddress(poolID string, subnet *string, ips []string) error {
	return nil
}

func (ts testCiaoService) RemoveAddress(poolID string, subnet *string, extIP *string) error {
	return nil
}

func (ts testCiaoService) ListMappedAddresses(tenant *string) []types.MappedIP {
	var ref string

	m := types.MappedIP{
		ID:         "ba58f471-0735-4773-9550-188e2d012941",
		ExternalIP: "192.168.0.1",
		InternalIP: "172.16.0.1",
		TenantID:   "8a497c68-a88a-4c1c-be56-12a4883208d3",
		PoolID:     "f384ffd8-e7bd-40c2-8552-2efbe7e3ad6e",
		PoolName:   "mypool",
	}

	if tenant != nil {
		ref = fmt.Sprintf("%s/external-ips/%s", *tenant, m.ID)
	} else {
		ref = fmt.Sprintf("/external-ips/%s", m.ID)
	}

	link := types.Link{
		Rel:  "self",
		Href: ref,
	}

	m.Links = []types.Link{link}

	if tenant == nil {
		ref := fmt.Sprintf("/pools/%s", m.PoolID)

		link := types.Link{
			Rel:  "pool",
			Href: ref,
		}

		m.Links = append(m.Links, link)
	}

	return []types.MappedIP{m}
}

func (ts testCiaoService) MapAddress(tenantID string, name *string, instanceID string) error {
	return nil
}

func (ts testCiaoService) UnMapAddress(string) error {
	return nil
}

func (ts testCiaoService) CreateWorkload(req types.Workload) (types.Workload, error) {
	req.ID = "ba58f471-0735-4773-9550-188e2d012941"
	return req, nil
}

func (ts testCiaoService) DeleteWorkload(tenant string, workload string) error {
	return nil
}

func (ts testCiaoService) ShowWorkload(tenant string, ID string) (types.Workload, error) {
	return types.Workload{
		ID:          "ba58f471-0735-4773-9550-188e2d012941",
		TenantID:    tenant,
		Description: "testWorkload",
		FWType:      payloads.Legacy,
		VMType:      payloads.QEMU,
		Config:      "this will totally work!",
		Visibility:  types.Private,
	}, nil
}

func (ts testCiaoService) ListWorkloads(tenant string) ([]types.Workload, error) {
	return []types.Workload{
		{
			ID:          "ba58f471-0735-4773-9550-188e2d012941",
			TenantID:    tenant,
			Description: "testWorkload",
			FWType:      payloads.Legacy,
			VMType:      payloads.QEMU,
			Config:      "this will totally work!",
			Visibility:  types.Private,
		},
	}, nil
}

func (ts testCiaoService) ListQuotas(tenantID string) []types.QuotaDetails {
	return []types.QuotaDetails{
		{Name: "test-quota-1", Value: 10, Usage: 3},
		{Name: "test-quota-2", Value: -1, Usage: 10},
		{Name: "test-limit", Value: 123, Usage: 0},
	}
}

func (ts testCiaoService) EvacuateNode(nodeID string) error {
	return nil
}

func (ts testCiaoService) RestoreNode(nodeID string) error {
	return nil
}

func (ts testCiaoService) UpdateQuotas(tenantID string, qds []types.QuotaDetails) error {
	return nil
}

func (ts testCiaoService) ListTenants() ([]types.TenantSummary, error) {
	summary := types.TenantSummary{
		ID:   "bc70dcd6-7298-4933-98a9-cded2d232d02",
		Name: "Test Tenant",
	}

	ref := fmt.Sprintf("/tenants/%s", summary.ID)

	link := types.Link{
		Rel:  "self",
		Href: ref,
	}

	summary.Links = append(summary.Links, link)

	return []types.TenantSummary{summary}, nil
}

func (ts testCiaoService) ShowTenant(ID string) (types.TenantConfig, error) {
	config := types.TenantConfig{
		Name:       "Test Tenant",
		SubnetBits: 24,
	}

	return config, nil
}

func (ts testCiaoService) PatchTenant(string, []byte) error {
	return nil
}

func (ts testCiaoService) CreateTenant(ID string, config types.TenantConfig) (types.TenantSummary, error) {
	summary := types.TenantSummary{
		ID:   ID,
		Name: config.Name,
	}

	ref := fmt.Sprintf("/tenants/%s", summary.ID)
	link := types.Link{
		Rel:  "self",
		Href: ref,
	}
	summary.Links = append(summary.Links, link)

	return summary, nil
}

func (ts testCiaoService) DeleteTenant(string) error {
	return nil
}

func (ts testCiaoService) CreateImage(tenantID string, req CreateImageRequest) (types.Image, error) {
	name := "Ubuntu"
	createdAt, _ := time.Parse(time.RFC3339, "2015-11-29T22:21:42Z")

	return types.Image{
		State:      types.Created,
		CreateTime: createdAt,
		Visibility: types.Private,
		ID:         "b2173dd3-7ad6-4362-baa6-a68bce3565cb",
		Name:       name,
	}, nil
}

func (ts testCiaoService) ListImages(tenantID string) ([]types.Image, error) {
	name := "Ubuntu"
	createdAt, _ := time.Parse(time.RFC3339, "2015-11-29T22:21:42Z")

	image := types.Image{
		State:      types.Created,
		CreateTime: createdAt,
		ID:         "b2173dd3-7ad6-4362-baa6-a68bce3565cb",
		Name:       name,
		Visibility: types.Public,
	}

	var images []types.Image
	images = append(images, image)

	return images, nil
}

func (ts testCiaoService) GetImage(tenantID, ID string) (types.Image, error) {
	imageID := "1bea47ed-f6a9-463b-b423-14b9cca9ad27"
	name := "cirros-0.3.2-x86_64-disk"
	createdAt, _ := time.Parse(time.RFC3339, "2014-05-05T17:15:10Z")
	var size uint64 = 13167616

	return types.Image{
		State:      types.Active,
		CreateTime: createdAt,
		Visibility: types.Public,
		ID:         imageID,
		Name:       name,
		Size:       size,
	}, nil
}

func (ts testCiaoService) UploadImage(string, string, io.Reader) error {
	return nil
}

func (ts testCiaoService) DeleteImage(string, string) error {
	return nil
}

func (ts testCiaoService) ShowVolumeDetails(tenant string, volume string) (types.Volume, error) {
	return types.Volume{
		BlockDevice: storage.BlockDevice{
			ID:   "new-test-id",
			Size: 123456,
		},
		State:       types.Available,
		Name:        "my volume",
		Description: "my volume for stuff",
		TenantID:    "test-tenant-id",
	}, nil
}

func (ts testCiaoService) CreateVolume(tenant string, req RequestedVolume) (types.Volume, error) {
	return types.Volume{
		BlockDevice: storage.BlockDevice{
			ID:   "new-test-id",
			Size: 123456,
		},
		State:       types.Available,
		Name:        "new volume",
		Description: "newly created volume",
		TenantID:    "test-tenant-id",
	}, nil
}

func (ts testCiaoService) DeleteVolume(tenant string, volume string) error {
	return nil
}

func (ts testCiaoService) AttachVolume(tenant string, volume string, instance string, mountpoint string) error {
	return nil
}

func (ts testCiaoService) DetachVolume(tenant string, volume string, attachment string) error {
	return nil
}

func (ts testCiaoService) ListVolumesDetail(tenant string) ([]types.Volume, error) {
	return []types.Volume{
		{
			BlockDevice: storage.BlockDevice{
				ID:   "new-test-id",
				Size: 123456,
			},
			State:       types.Available,
			Name:        "my volume",
			Description: "my volume for stuff",
			TenantID:    "test-tenant-id",
		},
		{
			BlockDevice: storage.BlockDevice{
				ID:   "new-test-id2",
				Size: 123456,
			},
			State:       types.Available,
			Name:        "volume 2",
			Description: "my other volume",
			TenantID:    "test-tenant-id",
		},
	}, nil
}

func (ts testCiaoService) CreateServer(tenant string, req CreateServerRequest) (interface{}, error) {
	req.Server.ID = "validServerID"
	return req, nil
}

func (ts testCiaoService) ListServersDetail(tenant string) ([]ServerDetails, error) {
	var servers []ServerDetails

	server := ServerDetails{
		NodeID:     "nodeUUID",
		ID:         "testUUID",
		TenantID:   tenant,
		WorkloadID: "testWorkloadUUID",
		Status:     "active",
		PrivateAddresses: []PrivateAddresses{
			{
				Addr:    "192.169.0.1",
				MacAddr: "00:02:00:01:02:03",
			},
		},
	}

	servers = append(servers, server)

	return servers, nil
}

func (ts testCiaoService) ShowServerDetails(tenant string, server string) (Server, error) {
	s := ServerDetails{
		NodeID:     "nodeUUID",
		ID:         server,
		TenantID:   tenant,
		WorkloadID: "testWorkloadUUID",
		Status:     "active",
		PrivateAddresses: []PrivateAddresses{
			{
				Addr:    "192.169.0.1",
				MacAddr: "00:02:00:01:02:03",
			},
		},
	}

	return Server{Server: s}, nil
}

func (ts testCiaoService) DeleteServer(tenant string, server string) error {
	return nil
}

func (ts testCiaoService) StartServer(tenant string, server string) error {
	return nil
}

func (ts testCiaoService) StopServer(tenant string, server string) error {
	return nil
}

func TestResponse(t *testing.T) {
	var ts testCiaoService

	mux := Routes(Config{"", ts}, nil)

	for i, tt := range tests {
		req, err := http.NewRequest(tt.method, tt.request, bytes.NewBuffer([]byte(tt.requestBody)))
		if err != nil {
			t.Fatal(err)
		}

		req = req.WithContext(service.SetPrivilege(req.Context(), true))

		rr := httptest.NewRecorder()
		req.Header.Set("Content-Type", tt.media)

		mux.ServeHTTP(rr, req)

		status := rr.Code
		if status != tt.expectedStatus {
			t.Errorf("test %d: got %v, expected %v", i, status, tt.expectedStatus)
		}

		if rr.Body.String() != tt.expectedResponse {
			t.Errorf("test %d: %s: failed\ngot: %v\nexp: %v", i, tt.request, rr.Body.String(), tt.expectedResponse)
		}
	}
}

func TestRoutes(t *testing.T) {
	var ts testCiaoService
	config := Config{"", ts}

	r := Routes(config, nil)
	if r == nil {
		t.Fatalf("No routes returned")
	}
}

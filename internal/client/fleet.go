// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
)

// The fleet inventory (platform 1.0.400), read only: the projects' VMs, the
// infrastructure VM nodes, the Kubernetes clusters and the certificates.

// VMFilter narrows GET /vms; empty fields do not filter.
type VMFilter struct {
	ProjectID, Environment, Role, Status string
}

// ListVMs reads every VM matching f, in id order.
func (a *API) ListVMs(ctx context.Context, f VMFilter) ([]Vm, error) {
	limit := a.pageSize()
	params := &VmsListParams{Limit: &limit}
	if f.ProjectID != "" {
		params.ProjectId = &f.ProjectID
	}
	if f.Environment != "" {
		e := VmsListParamsEnvironment(f.Environment)
		params.Environment = &e
	}
	if f.Role != "" {
		params.Role = &f.Role
	}
	if f.Status != "" {
		s := VmsListParamsStatus(f.Status)
		params.Status = &s
	}
	var all []Vm
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("GET /vms: gave up after %d pages; the cursor never ended", maxPages)
		}
		rsp, err := a.raw.VmsListWithResponse(ctx, params)
		if err != nil {
			return nil, err
		}
		if rsp.JSON200 == nil {
			return nil, unexpected("GET /vms", rsp.HTTPResponse)
		}
		all = append(all, rsp.JSON200.Items...)
		next := rsp.JSON200.NextCursor
		if next == nil || *next == "" {
			return all, nil
		}
		params.Cursor = next
	}
}

// ListVMNodes reads every infrastructure VM node, in id order.
func (a *API) ListVMNodes(ctx context.Context) ([]VmNode, error) {
	limit := a.pageSize()
	params := &VmNodesListParams{Limit: &limit}
	var all []VmNode
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("GET /vm-nodes: gave up after %d pages; the cursor never ended", maxPages)
		}
		rsp, err := a.raw.VmNodesListWithResponse(ctx, params)
		if err != nil {
			return nil, err
		}
		if rsp.JSON200 == nil {
			return nil, unexpected("GET /vm-nodes", rsp.HTTPResponse)
		}
		all = append(all, rsp.JSON200.Items...)
		next := rsp.JSON200.NextCursor
		if next == nil || *next == "" {
			return all, nil
		}
		params.Cursor = next
	}
}

// ListKubernetesClusters reads the clusters (one page, by name).
func (a *API) ListKubernetesClusters(ctx context.Context) ([]KubernetesCluster, error) {
	rsp, err := a.raw.KubernetesClustersListWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if rsp.JSON200 == nil {
		return nil, unexpected("GET /kubernetes-clusters", rsp.HTTPResponse)
	}
	return rsp.JSON200.Items, nil
}

// CertificateFilter narrows GET /certificates; empty fields do not filter.
type CertificateFilter struct {
	ProjectID, Status string
}

// ListCertificates reads every certificate matching f, in id order.
func (a *API) ListCertificates(ctx context.Context, f CertificateFilter) ([]Certificate, error) {
	limit := a.pageSize()
	params := &CertificatesListParams{Limit: &limit}
	if f.ProjectID != "" {
		params.ProjectId = &f.ProjectID
	}
	if f.Status != "" {
		s := CertificatesListParamsStatus(f.Status)
		params.Status = &s
	}
	var all []Certificate
	for page := 0; ; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("GET /certificates: gave up after %d pages; the cursor never ended", maxPages)
		}
		rsp, err := a.raw.CertificatesListWithResponse(ctx, params)
		if err != nil {
			return nil, err
		}
		if rsp.JSON200 == nil {
			return nil, unexpected("GET /certificates", rsp.HTTPResponse)
		}
		all = append(all, rsp.JSON200.Items...)
		next := rsp.JSON200.NextCursor
		if next == nil || *next == "" {
			return all, nil
		}
		params.Cursor = next
	}
}

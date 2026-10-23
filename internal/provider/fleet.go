// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// The fleet inventory (platform 1.0.400), read only: ataila_vms,
// ataila_vm_nodes, ataila_k8s_clusters, ataila_certificates.

var (
	_ datasource.DataSourceWithConfigure = (*fleetDataSource)(nil)

	vmTypes = map[string]attr.Type{
		"id": types.StringType, "project_id": types.StringType, "name": types.StringType,
		"environment": types.StringType, "role": types.StringType, "status": types.StringType,
		"cpu": types.Int64Type, "ram_gb": types.Int64Type, "disk_gb": types.Int64Type,
		"disk_class": types.StringType, "pending_hardware": types.BoolType, "created_at": types.StringType,
	}
	vmNodeTypes = map[string]attr.Type{
		"id": types.StringType, "name": types.StringType, "role": types.StringType,
		"environment": types.StringType, "status": types.StringType, "os": types.StringType,
		"cpu": types.Int64Type, "ram_gb": types.Int64Type, "disk_gb": types.Int64Type,
		"description": types.StringType, "created_at": types.StringType, "updated_at": types.StringType,
	}
	k8sClusterTypes = map[string]attr.Type{
		"name": types.StringType, "enabled": types.BoolType, "single_node": types.BoolType,
		"tenant_gpu_cards": types.Int64Type, "default_for_envs": types.ListType{ElemType: types.StringType},
		"whole_project": types.BoolType, "is_default_home": types.BoolType, "targetable": types.BoolType,
	}
	certificateTypes = map[string]attr.Type{
		"id": types.StringType, "project_id": types.StringType, "fqdn": types.StringType,
		"issuer": types.StringType, "challenge_type": types.StringType, "issued_at": types.StringType,
		"expires_at": types.StringType, "auto_rotate": types.BoolType,
		"last_renewal_attempt": types.StringType, "status": types.StringType, "created_at": types.StringType,
	}
)

// fleetDataSource is one of the four inventory reads; kind picks which.
type fleetDataSource struct {
	data *ProviderData
	kind string // vms | vm_nodes | k8s_clusters | certificates
}

// NewVMsDataSource is the factory for ataila_vms.
func NewVMsDataSource() datasource.DataSource { return &fleetDataSource{kind: "vms"} }

// NewVMNodesDataSource is the factory for ataila_vm_nodes.
func NewVMNodesDataSource() datasource.DataSource { return &fleetDataSource{kind: "vm_nodes"} }

// NewK8sClustersDataSource is the factory for ataila_k8s_clusters.
func NewK8sClustersDataSource() datasource.DataSource { return &fleetDataSource{kind: "k8s_clusters"} }

// NewCertificatesDataSource is the factory for ataila_certificates.
func NewCertificatesDataSource() datasource.DataSource { return &fleetDataSource{kind: "certificates"} }

func (d *fleetDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.kind
}

func (d *fleetDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func fleetOut(desc string) dschema.StringAttribute {
	return dschema.StringAttribute{MarkdownDescription: desc, Computed: true}
}

func fleetInt(desc string) dschema.Int64Attribute {
	return dschema.Int64Attribute{MarkdownDescription: desc, Computed: true}
}

func fleetBool(desc string) dschema.BoolAttribute {
	return dschema.BoolAttribute{MarkdownDescription: desc, Computed: true}
}

func fleetFilter(desc string, v ...validator.String) dschema.StringAttribute {
	return dschema.StringAttribute{MarkdownDescription: desc, Optional: true, Validators: v}
}

const fleetPlatform = "Platform release 1.0.400 or later."

func (d *fleetDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	project := fleetFilter("Only this project's.",
		stringvalidator.RegexMatches(rxIntID, "must be a project id"))
	switch d.kind {
	case "vms":
		resp.Schema = dschema.Schema{
			MarkdownDescription: "The projects' virtual machines, in id order: name, environment, role, status and " +
				"size. No address, host or machine id. Filters combine with AND.\n\nNeeds " +
				"`infrastructure-read-global`; a tenant-scoped token reads its own tenant's projects' VMs. " +
				fleetPlatform,
			Attributes: map[string]dschema.Attribute{
				"project_id": project,
				"environment": fleetFilter("Only this environment's: `dev`, `uat` or `prod`.",
					stringvalidator.OneOf("dev", "uat", "prod")),
				"role": fleetFilter("Only VMs of this role, e.g. `db`.", stringvalidator.LengthBetween(1, 80)),
				"status": fleetFilter("Only VMs in this status: `planned`, `provisioning`, `running`, `stopped`, "+
					"`failed` or `retired`.",
					stringvalidator.OneOf("planned", "provisioning", "running", "stopped", "failed", "retired")),
				"vms": dschema.ListNestedAttribute{
					MarkdownDescription: "The matching VMs, in id order.",
					Computed:            true,
					NestedObject: dschema.NestedAttributeObject{Attributes: map[string]dschema.Attribute{
						"id": fleetOut("The VM's id."), "project_id": fleetOut("The project it belongs to."),
						"name":        fleetOut("Its host name."),
						"environment": fleetOut("`dev`, `uat` or `prod`."),
						"role":        fleetOut("What it runs."),
						"status":      fleetOut("`planned`, `provisioning`, `running`, `stopped`, `failed` or `retired`."),
						"cpu":         fleetInt("Virtual CPUs."), "ram_gb": fleetInt("Memory in GB."),
						"disk_gb": fleetInt("Disk in GB."),
						"disk_class": fleetOut("`N` (local, replicated by the software on it) or `C` (network SSD); " +
							"null for a VM with no disk."),
						"pending_hardware": fleetBool("True while the VM waits for its disk hardware."),
						"created_at":       fleetOut("When it was recorded (RFC 3339, UTC)."),
					}},
				},
			},
		}
	case "vm_nodes":
		resp.Schema = dschema.Schema{
			MarkdownDescription: "The platform's own infrastructure virtual machines, in id order. No address, host " +
				"or machine id.\n\nNeeds `infrastructure-read-global`; never readable with a tenant-scoped token. " +
				fleetPlatform,
			Attributes: map[string]dschema.Attribute{
				"nodes": dschema.ListNestedAttribute{
					MarkdownDescription: "The nodes, in id order.",
					Computed:            true,
					NestedObject: dschema.NestedAttributeObject{Attributes: map[string]dschema.Attribute{
						"id": fleetOut("The node's id."), "name": fleetOut("Its host name."),
						"role": fleetOut("What it runs."), "environment": fleetOut("Its environment."),
						"status": fleetOut("Its state in the inventory."), "os": fleetOut("Its operating system."),
						"cpu": fleetInt("Virtual CPUs."), "ram_gb": fleetInt("Memory in GB."),
						"disk_gb": fleetInt("Disk in GB."), "description": fleetOut("What it is for."),
						"created_at": fleetOut("When it was recorded (RFC 3339, UTC)."),
						"updated_at": fleetOut("When its record last changed."),
					}},
				},
			},
		}
	case "k8s_clusters":
		resp.Schema = dschema.Schema{
			MarkdownDescription: "The Kubernetes clusters projects can run on, by name, the disabled ones included. " +
				"No endpoint or address.\n\nNeeds `infrastructure-read-global`; never readable with a tenant-scoped " +
				"token. " + fleetPlatform,
			Attributes: map[string]dschema.Attribute{
				"clusters": dschema.ListNestedAttribute{
					MarkdownDescription: "The clusters, by name.",
					Computed:            true,
					NestedObject: dschema.NestedAttributeObject{Attributes: map[string]dschema.Attribute{
						"name":             fleetOut("The cluster's name: what a Kubernetes project names as its target."),
						"enabled":          fleetBool("False while projects may not use it."),
						"single_node":      fleetBool("True for a one-node cluster."),
						"tenant_gpu_cards": fleetInt("GPU cards it offers to tenants' workloads."),
						"default_for_envs": dschema.ListAttribute{MarkdownDescription: "The environments a project " +
							"that names no cluster runs here.", ElementType: types.StringType, Computed: true},
						"whole_project":   fleetBool("True when a project targeted here has every environment on it."),
						"is_default_home": fleetBool("True for the cluster a project lands on when it names none."),
						"targetable":      fleetBool("True when a project may be homed here."),
					}},
				},
			},
		}
	case "certificates":
		resp.Schema = dschema.Schema{
			MarkdownDescription: "The TLS certificates of the domains, in id order: host name, issuer, expiry and " +
				"renewal state. Never a key. Filters combine with AND.\n\nNeeds `certificates-read-global`; a " +
				"tenant-scoped token reads its own tenant's projects' certificates. " + fleetPlatform,
			Attributes: map[string]dschema.Attribute{
				"project_id": project,
				"status": fleetFilter("Only certificates in this status: `valid`, `expiring`, `expired`, "+
					"`rotation_failed` or `revoked`.",
					stringvalidator.OneOf("valid", "expiring", "expired", "rotation_failed", "revoked")),
				"certificates": dschema.ListNestedAttribute{
					MarkdownDescription: "The matching certificates, in id order.",
					Computed:            true,
					NestedObject: dschema.NestedAttributeObject{Attributes: map[string]dschema.Attribute{
						"id":                   fleetOut("The certificate's id."),
						"project_id":           fleetOut("The project whose domain it covers; null for a domain no project owns."),
						"fqdn":                 fleetOut("The host name it covers, e.g. `shop.example.com` or `*.example.com`."),
						"issuer":               fleetOut("`letsencrypt`, `zerossl`, `self-signed` or `commercial`."),
						"challenge_type":       fleetOut("`dns-01` or `http-01`."),
						"issued_at":            fleetOut("When it was issued (RFC 3339, UTC)."),
						"expires_at":           fleetOut("When it expires."),
						"auto_rotate":          fleetBool("True when the platform renews it before it expires."),
						"last_renewal_attempt": fleetOut("When a renewal was last tried."),
						"status":               fleetOut("`valid`, `expiring`, `expired`, `rotation_failed` or `revoked`."),
						"created_at":           fleetOut("When it was recorded."),
					}},
				},
			},
		}
	}
}

func (d *fleetDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	if dg := d.data.featureRefused(client.FeatureFleet, path.Root(map[string]string{"vms": "vms",
		"vm_nodes": "nodes", "k8s_clusters": "clusters", "certificates": "certificates"}[d.kind]),
		"The ataila_"+d.kind+" data source"); dg != nil {
		resp.Diagnostics.Append(dg)
		return
	}
	switch d.kind {
	case "vms":
		d.readVMs(ctx, req, resp)
	case "vm_nodes":
		d.readNodes(ctx, resp)
	case "k8s_clusters":
		d.readClusters(ctx, resp)
	case "certificates":
		d.readCertificates(ctx, req, resp)
	}
}

func listOf(objType map[string]attr.Type, elems []attr.Value) types.List {
	return types.ListValueMust(types.ObjectType{AttrTypes: objType}, elems)
}

func (d *fleetDataSource) readVMs(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m struct {
		ProjectID   types.String `tfsdk:"project_id"`
		Environment types.String `tfsdk:"environment"`
		Role        types.String `tfsdk:"role"`
		Status      types.String `tfsdk:"status"`
		VMs         types.List   `tfsdk:"vms"`
	}
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vms, err := d.data.API.ListVMs(ctx, client.VMFilter{ProjectID: m.ProjectID.ValueString(),
		Environment: m.Environment.ValueString(), Role: m.Role.ValueString(), Status: m.Status.ValueString()})
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the virtual machines", err))
		return
	}
	elems := make([]attr.Value, 0, len(vms))
	for _, v := range vms {
		elems = append(elems, types.ObjectValueMust(vmTypes, map[string]attr.Value{
			"id": types.StringValue(v.Id), "project_id": types.StringValue(v.ProjectId),
			"name": types.StringValue(v.Name), "environment": types.StringValue(v.Environment),
			"role": types.StringValue(v.Role), "status": types.StringValue(v.Status),
			"cpu": int64OrNull(v.Cpu), "ram_gb": int64OrNull(v.RamGb), "disk_gb": int64OrNull(v.DiskGb),
			"disk_class": stringOrNull(v.DiskClass), "pending_hardware": types.BoolValue(v.PendingHardware),
			"created_at": types.StringValue(FormatTimestamp(v.CreatedAt)),
		}))
	}
	m.VMs = listOf(vmTypes, elems)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (d *fleetDataSource) readNodes(ctx context.Context, resp *datasource.ReadResponse) {
	nodes, err := d.data.API.ListVMNodes(ctx)
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the infrastructure VM nodes", err))
		return
	}
	elems := make([]attr.Value, 0, len(nodes))
	for _, n := range nodes {
		elems = append(elems, types.ObjectValueMust(vmNodeTypes, map[string]attr.Value{
			"id": types.StringValue(n.Id), "name": types.StringValue(n.Name), "role": types.StringValue(n.Role),
			"environment": types.StringValue(n.Environment), "status": types.StringValue(n.Status),
			"os": stringOrNull(n.Os), "cpu": int64OrNull(n.Cpu), "ram_gb": int64OrNull(n.RamGb),
			"disk_gb": int64OrNull(n.DiskGb), "description": stringOrNull(n.Description),
			"created_at": types.StringValue(FormatTimestamp(n.CreatedAt)),
			"updated_at": types.StringValue(FormatTimestamp(n.UpdatedAt)),
		}))
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("nodes"), listOf(vmNodeTypes, elems))...)
}

func (d *fleetDataSource) readClusters(ctx context.Context, resp *datasource.ReadResponse) {
	clusters, err := d.data.API.ListKubernetesClusters(ctx)
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the Kubernetes clusters", err))
		return
	}
	elems := make([]attr.Value, 0, len(clusters))
	for _, c := range clusters {
		elems = append(elems, types.ObjectValueMust(k8sClusterTypes, map[string]attr.Value{
			"name": types.StringValue(c.Name), "enabled": types.BoolValue(c.Enabled),
			"single_node": types.BoolValue(c.SingleNode), "tenant_gpu_cards": types.Int64Value(int64(c.TenantGpuCards)),
			"default_for_envs": stringsValue(c.DefaultForEnvs), "whole_project": types.BoolValue(c.WholeProject),
			"is_default_home": types.BoolValue(c.IsDefaultHome), "targetable": types.BoolValue(c.Targetable),
		}))
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("clusters"), listOf(k8sClusterTypes, elems))...)
}

func (d *fleetDataSource) readCertificates(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m struct {
		ProjectID    types.String `tfsdk:"project_id"`
		Status       types.String `tfsdk:"status"`
		Certificates types.List   `tfsdk:"certificates"`
	}
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	certs, err := d.data.API.ListCertificates(ctx, client.CertificateFilter{ProjectID: m.ProjectID.ValueString(),
		Status: m.Status.ValueString()})
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the certificates", err))
		return
	}
	elems := make([]attr.Value, 0, len(certs))
	for _, c := range certs {
		elems = append(elems, types.ObjectValueMust(certificateTypes, map[string]attr.Value{
			"id": types.StringValue(c.Id), "project_id": stringOrNull(c.ProjectId), "fqdn": types.StringValue(c.Fqdn),
			"issuer": types.StringValue(c.Issuer), "challenge_type": types.StringValue(c.ChallengeType),
			"issued_at": timestampString(c.IssuedAt), "expires_at": types.StringValue(FormatTimestamp(c.ExpiresAt)),
			"auto_rotate": types.BoolValue(c.AutoRotate), "last_renewal_attempt": timestampString(c.LastRenewalAttempt),
			"status": types.StringValue(c.Status), "created_at": types.StringValue(FormatTimestamp(c.CreatedAt)),
		}))
	}
	m.Certificates = listOf(certificateTypes, elems)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

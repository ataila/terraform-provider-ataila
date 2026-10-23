// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

var _ datasource.DataSourceWithConfigure = (*projectNetworkDataSource)(nil)

var (
	netVlanTypes = map[string]attr.Type{"vlan_id": types.Int64Type, "name": types.StringType,
		"cidr": types.StringType, "gateway": types.StringType, "site": types.StringType}
	netSubnetTypes = map[string]attr.Type{"cidr": types.StringType, "site": types.StringType,
		"environments": types.ListType{ElemType: types.StringType}}
	netRuleTypes = map[string]attr.Type{"position": types.Int64Type, "direction": types.StringType,
		"action": types.StringType, "source": types.StringType, "destination": types.StringType,
		"protocol": types.StringType, "port": types.StringType, "comment": types.StringType}
	netGroupTypes = map[string]attr.Type{"name": types.StringType, "environment": types.StringType,
		"cluster": types.StringType, "comment": types.StringType,
		"rules": types.ListType{ElemType: types.ObjectType{AttrTypes: netRuleTypes}}}
	netVMTypes = map[string]attr.Type{"name": types.StringType, "environment": types.StringType,
		"group": types.StringType, "enabled": types.BoolType, "applied": types.BoolType}
	netAllowTypes = map[string]attr.Type{"chain": types.StringType, "action": types.StringType,
		"source": types.StringType, "destination": types.StringType, "protocol": types.StringType,
		"ports": types.StringType, "comment": types.StringType}
	netPolicyTypes = map[string]attr.Type{"namespace": types.StringType, "name": types.StringType,
		"detail": types.StringType, "egress": types.ListType{ElemType: types.StringType}}
	netK8sTypes = map[string]attr.Type{"namespace": types.StringType,
		"policies": types.ListType{ElemType: types.ObjectType{AttrTypes: netPolicyTypes}}}
	netVnetVMTypes = map[string]attr.Type{"name": types.StringType, "environment": types.StringType,
		"address": types.StringType}
	netVnetTypes = map[string]attr.Type{"id": types.StringType, "environment": types.StringType,
		"vni": types.Int64Type, "cidr": types.StringType, "gateway": types.StringType,
		"applied": types.BoolType, "gateway_applied": types.BoolType,
		"vms": types.ListType{ElemType: types.ObjectType{AttrTypes: netVnetVMTypes}}}
	netVpcTypes = map[string]attr.Type{"name": types.StringType, "site": types.StringType,
		"pool_cidr": types.StringType, "vnets": types.ListType{ElemType: types.ObjectType{AttrTypes: netVnetTypes}}}
)

// NewProjectNetworkDataSource is the factory for ataila_project_network.
func NewProjectNetworkDataSource() datasource.DataSource { return &projectNetworkDataSource{} }

type projectNetworkDataSource struct {
	data *ProviderData
}

type projectNetworkModel struct {
	ProjectID         types.String `tfsdk:"project_id"`
	DeploymentBackend types.String `tfsdk:"deployment_backend"`
	NetworkMode       types.String `tfsdk:"network_mode"`
	Subnets           types.List   `tfsdk:"subnets"`
	Vlans             types.List   `tfsdk:"vlans"`
	SecurityGroups    types.List   `tfsdk:"security_groups"`
	VMs               types.List   `tfsdk:"vms"`
	GatewayAllows     types.List   `tfsdk:"gateway_allows"`
	Kubernetes        types.Object `tfsdk:"kubernetes"`
	VPC               types.Object `tfsdk:"vpc"`
}

func (d *projectNetworkDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_network"
}

func netStr(desc string) dschema.StringAttribute {
	return dschema.StringAttribute{MarkdownDescription: desc, Computed: true}
}

func netBool(desc string) dschema.BoolAttribute {
	return dschema.BoolAttribute{MarkdownDescription: desc, Computed: true}
}

func netInt(desc string) dschema.Int64Attribute {
	return dschema.Int64Attribute{MarkdownDescription: desc, Computed: true}
}

func netStrings(desc string) dschema.ListAttribute {
	return dschema.ListAttribute{MarkdownDescription: desc, ElementType: types.StringType, Computed: true}
}

func netList(desc string, attrs map[string]dschema.Attribute) dschema.ListNestedAttribute {
	return dschema.ListNestedAttribute{MarkdownDescription: desc, Computed: true,
		NestedObject: dschema.NestedAttributeObject{Attributes: attrs}}
}

func (d *projectNetworkDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "A project's network as the platform derives and applies it: the zone's subnets, the " +
			"VLANs, the per-VM security groups with every rule and which VM sits in which, the bounded allows on " +
			"the project's gateways, a Kubernetes project's network policies and a VPC-mode project's VPC " +
			"(preview).\n\n" +
			"Read only: the network follows the project's settings. A change of `ataila_project`'s exposure, " +
			"application gateway, UAT publishing or egress settings marks the network stages stale, and " +
			"`ataila_project_provisioning` applies them; until then the applied parts read here show the network " +
			"as it was.\n\n" +
			"Needs `networking-read-global`; a tenant-scoped token reads its own tenant's projects. Platform " +
			"release 1.0.401 or later.",
		Attributes: map[string]dschema.Attribute{
			"project_id": dschema.StringAttribute{
				MarkdownDescription: "Id of the project.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(rxIntID, "must be a project id")},
			},
			"deployment_backend": netStr("`vm` or `k8s`."),
			"network_mode":       netStr("`vlan`, or `vpc` for a VPC-mode project (preview)."),
			"subnets": netList("The zone's subnets (VM projects).", map[string]dschema.Attribute{
				"cidr": netStr("The subnet."), "site": netStr("The site it is on."),
				"environments": netStrings("The environments in it."),
			}),
			"vlans": netList("The project's VLANs, as applied.", map[string]dschema.Attribute{
				"vlan_id": netInt("The VLAN id."), "name": netStr("Its name."), "cidr": netStr("Its subnet."),
				"gateway": netStr("The project's gateway address on it."), "site": netStr("The site it is on."),
			}),
			"security_groups": netList("The per-VM security groups, as applied.", map[string]dschema.Attribute{
				"name": netStr("The group's name."), "environment": netStr("The environment it serves."),
				"cluster": netStr("The cluster it is applied on."), "comment": netStr("What the group is for."),
				"rules": netList("Its rules, in order (first match wins).", map[string]dschema.Attribute{
					"position": netInt("Its place in the group."), "direction": netStr("`in` or `out`."),
					"action":      netStr("`ACCEPT`, `DROP` or `REJECT`."),
					"source":      netStr("Where the traffic comes from; null for anywhere."),
					"destination": netStr("Where it goes; null for anywhere."),
					"protocol":    netStr("The protocol; null for any."),
					"port":        netStr("The destination port or range; null for any."),
					"comment":     netStr("What the rule is for."),
				}),
			}),
			"vms": netList("Each VM's group and firewall state.", map[string]dschema.Attribute{
				"name": netStr("The VM's host name."), "environment": netStr("Its environment."),
				"group": netStr("The security group it sits in."), "enabled": netBool("True when its firewall is on."),
				"applied": netBool("True once the platform has applied it."),
			}),
			"gateway_allows": netList("The bounded allows on the project's gateways, as applied, each once.",
				map[string]dschema.Attribute{
					"chain": netStr("`forward`, `input` or `output`."), "action": netStr("What the rule does."),
					"source":      netStr("Where the traffic comes from; null for anywhere."),
					"destination": netStr("Where it goes; null for anywhere."),
					"protocol":    netStr("The protocol; null for any."), "ports": netStr("The ports; null for any."),
					"comment": netStr("What the rule is for."),
				}),
			"kubernetes": dschema.SingleNestedAttribute{
				MarkdownDescription: "A Kubernetes project's network policies; null for a VM project.",
				Computed:            true,
				Attributes: map[string]dschema.Attribute{
					"namespace": netStr("The project's first namespace."),
					"policies": netList("The policies, per namespace: deny by default, then the allows.",
						map[string]dschema.Attribute{
							"namespace": netStr("The namespace it applies in."), "name": netStr("The policy's name."),
							"detail": netStr("What it allows or denies."),
							"egress": netStrings("For an egress allow: the dependencies it lets the pods reach."),
						}),
				},
			},
			"vpc": dschema.SingleNestedAttribute{
				MarkdownDescription: "A VPC-mode project's VPC (preview); null otherwise.",
				Computed:            true,
				Attributes: map[string]dschema.Attribute{
					"name": netStr("The VPC's name."), "site": netStr("The site it is on."),
					"pool_cidr": netStr("The address pool its VNets are cut from."),
					"vnets": netList("Its VNets.", map[string]dschema.Attribute{
						"id": netStr("The VNet's id."), "environment": netStr("The environment it serves."),
						"vni": netInt("Its VXLAN network identifier."), "cidr": netStr("Its subnet."),
						"gateway":         netStr("Its gateway address."),
						"applied":         netBool("True once applied on its cluster."),
						"gateway_applied": netBool("True once its gateway is applied."),
						"vms": netList("The VMs on it.", map[string]dschema.Attribute{
							"name": netStr("The VM's host name."), "environment": netStr("Its environment."),
							"address": netStr("Its address on the VNet."),
						}),
					}),
				},
			},
		},
	}
}

func (d *projectNetworkDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func objList(t map[string]attr.Type, elems []attr.Value) types.List {
	return types.ListValueMust(types.ObjectType{AttrTypes: t}, elems)
}

func networkValues(n *client.ProjectNetwork) (subnets, vlans, groups, vms, allows types.List, k8s, vpc types.Object) {
	var e []attr.Value
	for _, s := range n.Subnets {
		e = append(e, types.ObjectValueMust(netSubnetTypes, map[string]attr.Value{
			"cidr": stringOrNull(s.Cidr), "site": stringOrNull(s.Site), "environments": stringsValue(s.Environments)}))
	}
	subnets = objList(netSubnetTypes, e)
	e = nil
	for _, v := range n.Vlans {
		e = append(e, types.ObjectValueMust(netVlanTypes, map[string]attr.Value{
			"vlan_id": types.Int64Value(int64(v.VlanId)), "name": stringOrNull(v.Name), "cidr": stringOrNull(v.Cidr),
			"gateway": stringOrNull(v.Gateway), "site": stringOrNull(v.Site)}))
	}
	vlans = objList(netVlanTypes, e)
	e = nil
	for _, g := range n.SecurityGroups {
		var rules []attr.Value
		for _, r := range g.Rules {
			rules = append(rules, types.ObjectValueMust(netRuleTypes, map[string]attr.Value{
				"position": int64OrNull(r.Position), "direction": stringOrNull(r.Direction),
				"action": stringOrNull(r.Action), "source": stringOrNull(r.Source),
				"destination": stringOrNull(r.Destination), "protocol": stringOrNull(r.Protocol),
				"port": stringOrNull(r.Port), "comment": stringOrNull(r.Comment)}))
		}
		e = append(e, types.ObjectValueMust(netGroupTypes, map[string]attr.Value{
			"name": types.StringValue(g.Name), "environment": stringOrNull(g.Environment),
			"cluster": stringOrNull(g.Cluster), "comment": stringOrNull(g.Comment), "rules": objList(netRuleTypes, rules)}))
	}
	groups = objList(netGroupTypes, e)
	e = nil
	for _, v := range n.Vms {
		e = append(e, types.ObjectValueMust(netVMTypes, map[string]attr.Value{
			"name": types.StringValue(v.Name), "environment": stringOrNull(v.Environment), "group": stringOrNull(v.Group),
			"enabled": types.BoolValue(v.Enabled), "applied": types.BoolValue(v.Applied)}))
	}
	vms = objList(netVMTypes, e)
	e = nil
	for _, a := range n.GatewayAllows {
		e = append(e, types.ObjectValueMust(netAllowTypes, map[string]attr.Value{
			"chain": types.StringValue(a.Chain), "action": types.StringValue(a.Action), "source": stringOrNull(a.Source),
			"destination": stringOrNull(a.Destination), "protocol": stringOrNull(a.Protocol),
			"ports": stringOrNull(a.Ports), "comment": stringOrNull(a.Comment)}))
	}
	allows = objList(netAllowTypes, e)
	k8s = types.ObjectNull(netK8sTypes)
	if n.Kubernetes != nil {
		var pols []attr.Value
		for _, p := range n.Kubernetes.Policies {
			pols = append(pols, types.ObjectValueMust(netPolicyTypes, map[string]attr.Value{
				"namespace": types.StringValue(p.Namespace), "name": types.StringValue(p.Name),
				"detail": stringOrNull(p.Detail), "egress": stringsValue(p.Egress)}))
		}
		k8s = types.ObjectValueMust(netK8sTypes, map[string]attr.Value{
			"namespace": stringOrNull(n.Kubernetes.Namespace), "policies": objList(netPolicyTypes, pols)})
	}
	vpc = types.ObjectNull(netVpcTypes)
	if n.Vpc != nil {
		var vnets []attr.Value
		for _, v := range n.Vpc.Vnets {
			var onIt []attr.Value
			for _, m := range v.Vms {
				onIt = append(onIt, types.ObjectValueMust(netVnetVMTypes, map[string]attr.Value{
					"name": stringOrNull(m.Name), "environment": stringOrNull(m.Environment),
					"address": stringOrNull(m.Address)}))
			}
			vnets = append(vnets, types.ObjectValueMust(netVnetTypes, map[string]attr.Value{
				"id": types.StringValue(v.Id), "environment": stringOrNull(v.Environment), "vni": int64OrNull(v.Vni),
				"cidr": stringOrNull(v.Cidr), "gateway": stringOrNull(v.Gateway), "applied": types.BoolValue(v.Applied),
				"gateway_applied": types.BoolValue(v.GatewayApplied), "vms": objList(netVnetVMTypes, onIt)}))
		}
		vpc = types.ObjectValueMust(netVpcTypes, map[string]attr.Value{
			"name": stringOrNull(n.Vpc.Name), "site": stringOrNull(n.Vpc.Site),
			"pool_cidr": stringOrNull(n.Vpc.PoolCidr), "vnets": objList(netVnetTypes, vnets)})
	}
	return subnets, vlans, groups, vms, allows, k8s, vpc
}

func (d *projectNetworkDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var m projectNetworkModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if dg := d.data.featureRefused(client.FeatureProjectNetwork, path.Root("project_id"),
		"The ataila_project_network data source"); dg != nil {
		resp.Diagnostics.Append(dg)
		return
	}
	pid := m.ProjectID.ValueString()
	n, err := d.data.API.GetProjectNetwork(ctx, pid)
	if err != nil {
		resp.Diagnostics.Append(apiError(fmt.Sprintf("reading the network of project %s", pid), err))
		return
	}
	m.DeploymentBackend = types.StringValue(n.DeploymentBackend)
	m.NetworkMode = types.StringValue(n.NetworkMode)
	m.Subnets, m.Vlans, m.SecurityGroups, m.VMs, m.GatewayAllows, m.Kubernetes, m.VPC = networkValues(n)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

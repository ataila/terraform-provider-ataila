// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// The members of a computed list or object of objects are nested attributes,
// so that each carries a description of its own: the provider's where it has
// one, else the contract's text for that property. The attribute types are
// those of the plain list or object they replace, so states and values are
// unchanged.

// nestedDoc describes a member by its path below the list or object.
type nestedDoc func(path []string) string

// contractDoc describes members from the contract schema of the objects.
func contractDoc(schema string) nestedDoc {
	return func(p []string) string { return client.Describe(schema, p...) }
}

// specDoc describes members from their fieldSpec, else from the contract
// schema of the objects.
func specDoc(specs []fieldSpec, schema string) nestedDoc {
	return func(p []string) string {
		level := specs
		var f *fieldSpec
		for _, name := range p {
			f = nil
			for i := range level {
				if level[i].name == name {
					f = &level[i]
					break
				}
			}
			if f == nil {
				break
			}
			level = f.sub
		}
		doc := ""
		if f != nil {
			doc = f.doc
		}
		if doc == "" {
			doc = client.Describe(schema, p...)
		}
		if f != nil && f.kind == fTime {
			doc = joinDoc(doc, "RFC 3339 in UTC.")
		}
		return doc
	}
}

func joinDoc(a, b string) string {
	if a == "" {
		return b
	}
	return a + " " + b
}

// dataNestedList is a computed data source list of objects.
func dataNestedList(desc string, types map[string]attr.Type, doc nestedDoc) dschema.ListNestedAttribute {
	return dschema.ListNestedAttribute{MarkdownDescription: desc, Computed: true,
		NestedObject: dschema.NestedAttributeObject{Attributes: dataNestedAttrs(types, doc, nil)}}
}

// dataNestedAttrs are the computed data source attributes of an object.
func dataNestedAttrs(types map[string]attr.Type, doc nestedDoc, at []string) map[string]dschema.Attribute {
	out := make(map[string]dschema.Attribute, len(types))
	for name, t := range types {
		p := append(append([]string{}, at...), name)
		d := doc(p)
		switch tt := t.(type) {
		case basetypes.StringType:
			out[name] = dschema.StringAttribute{MarkdownDescription: d, Computed: true}
		case basetypes.Int64Type:
			out[name] = dschema.Int64Attribute{MarkdownDescription: d, Computed: true}
		case basetypes.Float64Type:
			out[name] = dschema.Float64Attribute{MarkdownDescription: d, Computed: true}
		case basetypes.BoolType:
			out[name] = dschema.BoolAttribute{MarkdownDescription: d, Computed: true}
		case basetypes.MapType:
			out[name] = dschema.MapAttribute{MarkdownDescription: d, Computed: true, ElementType: tt.ElemType}
		case basetypes.ObjectType:
			out[name] = dschema.SingleNestedAttribute{MarkdownDescription: d, Computed: true,
				Attributes: dataNestedAttrs(tt.AttrTypes, doc, p)}
		case basetypes.ListType:
			if ot, ok := tt.ElemType.(basetypes.ObjectType); ok {
				out[name] = dschema.ListNestedAttribute{MarkdownDescription: d, Computed: true,
					NestedObject: dschema.NestedAttributeObject{Attributes: dataNestedAttrs(ot.AttrTypes, doc, p)}}
			} else {
				out[name] = dschema.ListAttribute{MarkdownDescription: d, Computed: true, ElementType: tt.ElemType}
			}
		default:
			panic(fmt.Sprintf("nested attribute %v: no schema attribute for type %T", p, t))
		}
	}
	return out
}

// resourceNestedList is a computed resource list of objects.
func resourceNestedList(desc string, types map[string]attr.Type, doc nestedDoc, mods []planmodifier.List) rschema.ListNestedAttribute {
	return rschema.ListNestedAttribute{MarkdownDescription: desc, Computed: true, PlanModifiers: mods,
		NestedObject: rschema.NestedAttributeObject{Attributes: resourceNestedAttrs(types, doc, nil)}}
}

// resourceNestedAttrs are the computed resource attributes of an object.
func resourceNestedAttrs(types map[string]attr.Type, doc nestedDoc, at []string) map[string]rschema.Attribute {
	out := make(map[string]rschema.Attribute, len(types))
	for name, t := range types {
		p := append(append([]string{}, at...), name)
		d := doc(p)
		switch tt := t.(type) {
		case basetypes.StringType:
			out[name] = rschema.StringAttribute{MarkdownDescription: d, Computed: true}
		case basetypes.Int64Type:
			out[name] = rschema.Int64Attribute{MarkdownDescription: d, Computed: true}
		case basetypes.Float64Type:
			out[name] = rschema.Float64Attribute{MarkdownDescription: d, Computed: true}
		case basetypes.BoolType:
			out[name] = rschema.BoolAttribute{MarkdownDescription: d, Computed: true}
		case basetypes.MapType:
			out[name] = rschema.MapAttribute{MarkdownDescription: d, Computed: true, ElementType: tt.ElemType}
		case basetypes.ObjectType:
			out[name] = rschema.SingleNestedAttribute{MarkdownDescription: d, Computed: true,
				Attributes: resourceNestedAttrs(tt.AttrTypes, doc, p)}
		case basetypes.ListType:
			if ot, ok := tt.ElemType.(basetypes.ObjectType); ok {
				out[name] = rschema.ListNestedAttribute{MarkdownDescription: d, Computed: true,
					NestedObject: rschema.NestedAttributeObject{Attributes: resourceNestedAttrs(ot.AttrTypes, doc, p)}}
			} else {
				out[name] = rschema.ListAttribute{MarkdownDescription: d, Computed: true, ElementType: tt.ElemType}
			}
		default:
			panic(fmt.Sprintf("nested attribute %v: no schema attribute for type %T", p, t))
		}
	}
	return out
}

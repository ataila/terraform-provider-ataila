// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Read-only API objects with many members (AI Center, AI models) are mapped
// to attributes from a spec: one line per member.

type fieldKind int

const (
	fString    fieldKind = iota
	fFloat               // a number, kept as float64
	fInt                 // a whole number
	fBool                //
	fTime                // an RFC 3339 time, normalised to UTC
	fStrings             // a list of strings (nulls in it are dropped)
	fObject              // a nested object (sub)
	fObjects             // a list of nested objects (sub)
	fStringMap           // an object of scalars, as strings
)

type fieldSpec struct {
	name string
	kind fieldKind
	doc  string
	sub  []fieldSpec
}

func (f fieldSpec) attrType() attr.Type {
	switch f.kind {
	case fFloat:
		return types.Float64Type
	case fInt:
		return types.Int64Type
	case fBool:
		return types.BoolType
	case fStrings:
		return types.ListType{ElemType: types.StringType}
	case fObject:
		return types.ObjectType{AttrTypes: specTypes(f.sub)}
	case fObjects:
		return types.ListType{ElemType: types.ObjectType{AttrTypes: specTypes(f.sub)}}
	case fStringMap:
		return types.MapType{ElemType: types.StringType}
	}
	return types.StringType
}

func specTypes(specs []fieldSpec) map[string]attr.Type {
	out := make(map[string]attr.Type, len(specs))
	for _, f := range specs {
		out[f.name] = f.attrType()
	}
	return out
}

// scalarString renders a JSON scalar as a string: numbers without a
// trailing ".0", booleans as true/false.
func scalarString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	case nil:
		return "", false
	}
	return fmt.Sprint(v), true
}

// specValue converts one JSON value to its attribute value.
func specValue(f fieldSpec, raw any) attr.Value {
	switch f.kind {
	case fFloat:
		if n, ok := raw.(float64); ok {
			return types.Float64Value(n)
		}
		return types.Float64Null()
	case fInt:
		if n, ok := raw.(float64); ok {
			return types.Int64Value(int64(n))
		}
		return types.Int64Null()
	case fBool:
		if b, ok := raw.(bool); ok {
			return types.BoolValue(b)
		}
		return types.BoolNull()
	case fTime:
		s, _ := raw.(string)
		if s == "" {
			return types.StringNull()
		}
		if t, err := ParseTimestamp(s); err == nil {
			return types.StringValue(FormatTimestamp(t))
		}
		return types.StringValue(s)
	case fStrings:
		list, ok := raw.([]any)
		if !ok {
			if raw == nil {
				return types.ListNull(types.StringType)
			}
		}
		elems := make([]attr.Value, 0, len(list))
		for _, x := range list {
			if s, ok := scalarString(x); ok {
				elems = append(elems, types.StringValue(s))
			}
		}
		return types.ListValueMust(types.StringType, elems)
	case fObject:
		m, ok := raw.(map[string]any)
		if !ok {
			return types.ObjectNull(specTypes(f.sub))
		}
		return types.ObjectValueMust(specTypes(f.sub), specValues(m, f.sub))
	case fObjects:
		ot := types.ObjectType{AttrTypes: specTypes(f.sub)}
		list, ok := raw.([]any)
		if !ok && raw == nil {
			return types.ListNull(ot)
		}
		elems := make([]attr.Value, 0, len(list))
		for _, x := range list {
			m, _ := x.(map[string]any)
			elems = append(elems, types.ObjectValueMust(ot.AttrTypes, specValues(m, f.sub)))
		}
		return types.ListValueMust(ot, elems)
	case fStringMap:
		m, ok := raw.(map[string]any)
		if !ok {
			return types.MapNull(types.StringType)
		}
		elems := make(map[string]attr.Value, len(m))
		for k, x := range m {
			s, _ := scalarString(x)
			elems[k] = types.StringValue(s)
		}
		return types.MapValueMust(types.StringType, elems)
	}
	if s, ok := scalarString(raw); ok {
		return types.StringValue(s)
	}
	return types.StringNull()
}

func specValues(m map[string]any, specs []fieldSpec) map[string]attr.Value {
	out := make(map[string]attr.Value, len(specs))
	for _, f := range specs {
		out[f.name] = specValue(f, m[f.name])
	}
	return out
}

// specDataAttributes are computed data source attributes for the specs, of
// the objects of the contract schema named: nested objects and lists of
// objects are nested attributes, each member described by its spec or, where
// the spec has no text, by the contract.
func specDataAttributes(specs []fieldSpec, schema string) map[string]dschema.Attribute {
	return dataNestedAttrs(specTypes(specs), specDoc(specs, schema), nil)
}

// fieldNames lists the members of a nested spec for a description.
func fieldNames(specs []fieldSpec) string {
	s := ""
	for i, f := range specs {
		if i > 0 {
			s += ", "
		}
		s += "`" + f.name + "`"
	}
	return s
}

/*
Copyright 2020 The Vitess Authors.

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

package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"vitess.io/vitess/go/tools/graphviz"
	"vitess.io/vitess/go/vt/key"
	topodatapb "vitess.io/vitess/go/vt/proto/topodata"
	"vitess.io/vitess/go/vt/vtgate/vindexes"
)

const inputName = "InputName"

// PrimitiveDescription is used to create a serializable representation of the Primitive tree
// Using this structure, all primitives can share json marshalling code, which gives us an uniform output
type PrimitiveDescription struct {
	OperatorType string
	Variant      string
	// Keyspace specifies the keyspace to send the query to.
	Keyspace *vindexes.Keyspace
	// TargetDestination specifies an explicit target destination to send the query to.
	TargetDestination key.Destination
	// TargetTabletType specifies an explicit target destination tablet type
	// this is only used in conjunction with TargetDestination
	TargetTabletType topodatapb.TabletType
	Other            map[string]any

	InputName string
	Inputs    []PrimitiveDescription

	Stats RowsReceived
}

// MarshalJSON serializes the PlanDescription into a JSON representation.
// We do this rather manual thing here so the `other` map looks like
// fields belonging to pd and not a map in a field.
func (pd PrimitiveDescription) MarshalJSON() ([]byte, error) {
	buf := &bytes.Buffer{}
	buf.WriteString("{")

	prepend := ""
	if pd.InputName != "" {
		if err := marshalAdd(prepend, buf, "InputName", pd.InputName); err != nil {
			return nil, err
		}
		prepend = ","
	}
	if err := marshalAdd(prepend, buf, "OperatorType", pd.OperatorType); err != nil {
		return nil, err
	}
	prepend = ","
	if pd.Variant != "" {
		if err := marshalAdd(prepend, buf, "Variant", pd.Variant); err != nil {
			return nil, err
		}
	}
	if pd.Keyspace != nil {
		if err := marshalAdd(prepend, buf, "Keyspace", pd.Keyspace); err != nil {
			return nil, err
		}
	}
	if pd.TargetDestination != nil {
		s := pd.TargetDestination.String()
		dest := s[11:] // TODO: All these start with Destination. We should fix that instead if trimming it out here

		if err := marshalAdd(prepend, buf, "TargetDestination", dest); err != nil {
			return nil, err
		}
	}
	if pd.TargetTabletType != topodatapb.TabletType_UNKNOWN {
		if err := marshalAdd(prepend, buf, "TargetTabletType", pd.TargetTabletType.String()); err != nil {
			return nil, err
		}
	}
	if len(pd.Stats) > 0 {
		if err := marshalAdd(prepend, buf, "NoOfCalls", len(pd.Stats)); err != nil {
			return nil, err
		}

		if err := marshalAdd(prepend, buf, "AvgNumberOfRows", average(pd.Stats)); err != nil {
			return nil, err
		}
		if err := marshalAdd(prepend, buf, "MedianNumberOfRows", median(pd.Stats)); err != nil {
			return nil, err
		}
	}
	err := addMap(pd.Other, buf)
	if err != nil {
		return nil, err
	}

	if len(pd.Inputs) > 0 {
		if err := marshalAdd(prepend, buf, "Inputs", pd.Inputs); err != nil {
			return nil, err
		}
	}

	buf.WriteString("}")

	return buf.Bytes(), nil
}

func average(nums []int) float64 {
	total := 0
	for _, num := range nums {
		total += num
	}
	return float64(total) / float64(len(nums))
}

func median(nums []int) float64 {
	sortedNums := make([]int, len(nums))
	copy(sortedNums, nums)
	sort.Ints(sortedNums)

	n := len(sortedNums)
	if n%2 == 0 {
		mid1 := sortedNums[n/2-1]
		mid2 := sortedNums[n/2]
		return float64(mid1+mid2) / 2.0
	}
	return float64(sortedNums[n/2])
}

func (pd PrimitiveDescription) addToGraph(g *graphviz.Graph) (*graphviz.Node, error) {
	var nodes []*graphviz.Node
	for _, input := range pd.Inputs {
		n, err := input.addToGraph(g)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	name := pd.OperatorType + ":" + pd.Variant
	if pd.Variant == "" {
		name = pd.OperatorType
	}
	this := g.AddNode(name)
	for k, v := range pd.Other {
		switch k {
		case "Query":
			this.AddTooltip(fmt.Sprintf("%v", v))
		case "FieldQuery":
		// skip these
		default:
			slice, ok := v.([]string)
			if ok {
				this.AddAttribute(k)
				for _, s := range slice {
					this.AddAttribute(s)
				}
			} else {
				this.AddAttribute(fmt.Sprintf("%s:%v", k, v))
			}
		}
	}
	for _, n := range nodes {
		g.AddEdge(this, n)
	}
	return this, nil
}

func GraphViz(p Primitive) (*graphviz.Graph, error) {
	g := graphviz.New()
	description := PrimitiveToPlanDescription(p, nil)
	_, err := description.addToGraph(g)
	if err != nil {
		return nil, err
	}
	return g, nil
}

func addMap(input map[string]any, buf *bytes.Buffer) error {
	var mk []string
	for k, v := range input {
		if v == "" || v == nil || v == 0 || v == false {
			continue
		}
		mk = append(mk, k)
	}
	sort.Strings(mk)
	for _, k := range mk {
		v := input[k]
		if err := marshalAdd(",", buf, k, v); err != nil {
			return err
		}
	}
	return nil
}

func marshalAdd(prepend string, buf *bytes.Buffer, name string, obj any) error {
	buf.WriteString(prepend + `"` + name + `":`)

	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)

	return enc.Encode(obj)
}

// PrimitiveToPlanDescription transforms a primitive tree into a corresponding PlanDescription tree
// If stats is not nil, it will be used to populate the stats field of the PlanDescription
func PrimitiveToPlanDescription(in Primitive, stats map[Primitive]RowsReceived) PrimitiveDescription {
	this := in.description()
	if stats != nil {
		this.Stats = stats[in]
	}

	inputs, infos := in.Inputs()
	for idx, input := range inputs {
		pd := PrimitiveToPlanDescription(input, stats)
		if infos != nil {
			for k, v := range infos[idx] {
				if k == inputName {
					pd.InputName = v.(string)
					continue
				}
				if pd.Other == nil {
					pd.Other = map[string]any{}
				}
				pd.Other[k] = v
			}
		}
		this.Inputs = append(this.Inputs, pd)
	}

	if len(inputs) == 0 {
		this.Inputs = []PrimitiveDescription{}
	}

	return this
}

func orderedStringIntMap(in map[string]int) orderedMap {
	result := make(orderedMap, 0, len(in))
	for k, v := range in {
		result = append(result, keyVal{key: k, val: v})
	}
	sort.Sort(result)
	return result
}

type keyVal struct {
	key string
	val any
}

// Define an ordered, sortable map
type orderedMap []keyVal

func (m orderedMap) Len() int {
	return len(m)
}

func (m orderedMap) Less(i, j int) bool {
	return m[i].key < m[j].key
}

func (m orderedMap) Swap(i, j int) {
	m[i], m[j] = m[j], m[i]
}

var _ sort.Interface = (orderedMap)(nil)

func (m orderedMap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer

	buf.WriteString("{")
	for i, kv := range m {
		if i != 0 {
			buf.WriteString(",")
		}
		// marshal key
		key, err := json.Marshal(kv.key)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteString(":")
		// marshal value
		val, err := json.Marshal(kv.val)
		if err != nil {
			return nil, err
		}
		buf.Write(val)
	}

	buf.WriteString("}")
	return buf.Bytes(), nil
}

func (m orderedMap) String() string {
	var output []string
	for _, val := range m {
		output = append(output, fmt.Sprintf("%s:%v", val.key, val.val))
	}
	return strings.Join(output, " ")
}

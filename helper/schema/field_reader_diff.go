package schema

import (
	"fmt"
	"strings"

	"github.com/hashicorp/terraform/terraform"
	"github.com/mitchellh/mapstructure"
)

// DiffFieldReader reads fields out of a diff structures.
//
// It also requires access to a Reader that reads fields from the structure
// that the diff was derived from. This is usually the state. This is required
// because a diff on its own doesn't have complete data about full objects
// such as maps.
//
// The Source MUST be the data that the diff was derived from. If it isn't,
// the behavior of this struct is undefined.
//
// Reading fields from a DiffFieldReader is identical to reading from
// Source except the diff will be applied to the end result.
//
// The "Exists" field on the result will be set to true if the complete
// field exists whether its from the source, diff, or a combination of both.
// It cannot be determined whether a retrieved value is composed of
// diff elements.
type DiffFieldReader struct {
	Diff   *terraform.InstanceDiff
	Source FieldReader
	Schema map[string]*Schema
}

func (r *DiffFieldReader) ReadField(address []string) (FieldReadResult, error) {
	schemaList := addrToSchema(address, r.Schema)
	if len(schemaList) == 0 {
		return FieldReadResult{}, nil
	}

	schema := schemaList[len(schemaList)-1]
	switch schema.Type {
	case TypeBool:
		fallthrough
	case TypeInt:
		fallthrough
	case TypeFloat:
		fallthrough
	case TypeString:
		return r.readPrimitive(address, schema)
	case TypeList:
		return readListField(r, address, schema)
	case TypeMap:
		return r.readMap(address, schema)
	case TypeSet:
		return r.readSet(address, schema)
	case typeObject:
		return readObjectField(r, address, schema.Elem.(map[string]*Schema))
	default:
		panic(fmt.Sprintf("Unknown type: %#v", schema.Type))
	}
}

func (r *DiffFieldReader) readMap(
	address []string, schema *Schema) (FieldReadResult, error) {
	result := make(map[string]interface{})
	resultSet := false

	// First read the map from the underlying source
	source, err := r.Source.ReadField(address)
	if err != nil {
		return FieldReadResult{}, err
	}
	if source.Exists {
		result = source.Value.(map[string]interface{})
		resultSet = true
	}

	// Next, read all the elements we have in our diff, and apply
	// the diff to our result.
	prefix := strings.Join(address, ".") + "."
	for k, v := range r.Diff.Attributes {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		resultSet = true

		k = k[len(prefix):]
		if v.NewRemoved {
			delete(result, k)
			continue
		}

		result[k] = v.New
	}

	var resultVal interface{}
	if resultSet {
		resultVal = result
	}

	return FieldReadResult{
		Value:  resultVal,
		Exists: resultSet,
	}, nil
}

func (r *DiffFieldReader) readPrimitive(
	address []string, schema *Schema) (FieldReadResult, error) {
	result, err := r.Source.ReadField(address)
	if err != nil {
		return FieldReadResult{}, err
	}

	attrD, ok := r.Diff.Attributes[strings.Join(address, ".")]
	if !ok {
		return result, nil
	}

	var resultVal string
	if !attrD.NewComputed {
		resultVal = attrD.New
		if attrD.NewExtra != nil {
			result.ValueProcessed = resultVal
			if err := mapstructure.WeakDecode(attrD.NewExtra, &resultVal); err != nil {
				return FieldReadResult{}, err
			}
		}
	}

	result.Computed = attrD.NewComputed
	result.Exists = true
	result.Value, err = stringToPrimitive(resultVal, false, schema)
	if err != nil {
		return FieldReadResult{}, err
	}

	return result, nil
}

func (r *DiffFieldReader) readSet(
	address []string, schema *Schema) (FieldReadResult, error) {
	// Create the set that will be our result
	set := &Set{F: schema.Set}

	// Go through the map and find all the set items
	prefix := strings.Join(address, ".") + "."
	for k, _ := range r.Diff.Attributes {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if strings.HasPrefix(k, prefix+"#") {
			// Ignore the count field
			continue
		}

		// Split the key, since it might be a sub-object like "idx.field"
		parts := strings.Split(k[len(prefix):], ".")
		idx := parts[0]

		raw, err := r.ReadField(append(address, idx))
		if err != nil {
			return FieldReadResult{}, err
		}
		if !raw.Exists {
			// This shouldn't happen because we just verified it does exist
			panic("missing field in set: " + k + "." + idx)
		}

		set.Add(raw.Value)
	}

	return FieldReadResult{
		Value:  set,
		Exists: set.Len() > 0,
	}, nil
}

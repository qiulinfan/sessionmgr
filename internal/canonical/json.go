package canonical

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

func Marshal(v interface{}) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var normalized interface{}
	if err := dec.Decode(&normalized); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := encode(&out, normalized); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func encode(out *bytes.Buffer, v interface{}) error {
	switch value := v.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(value))
	case string:
		b, _ := json.Marshal(value)
		out.Write(b)
	case json.Number:
		out.WriteString(value.String())
	case []interface{}:
		out.WriteByte('[')
		for i, item := range value {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := encode(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]interface{}:
		keys := make([]string, 0, len(value))
		for k := range value {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			k, _ := json.Marshal(key)
			out.Write(k)
			out.WriteByte(':')
			if err := encode(out, value[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("canonical json: unsupported value %T", v)
	}
	return nil
}

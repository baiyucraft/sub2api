package service

import (
	"reflect"
	"time"
)

type accountListCloneIdentity struct {
	kind             reflect.Kind
	typ              reflect.Type
	pointer          uintptr
	length, capacity int
}

// cloneAccountListSnapshot preserves scalar/interface types, nil values and
// relation cycles without serializing credentials. Private runtime caches are
// intentionally reset: they point into the original credential maps.
func cloneAccountListSnapshot(accounts []Account) []Account {
	if accounts == nil {
		return nil
	}
	cloned := cloneAccountListValue(reflect.ValueOf(accounts), make(map[accountListCloneIdentity]reflect.Value))
	return cloned.Interface().([]Account)
}

func cloneAccountListValue(value reflect.Value, seen map[accountListCloneIdentity]reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.New(value.Type()).Elem()
		out.Set(cloneAccountListValue(value.Elem(), seen))
		return out
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		id := accountListCloneIdentity{kind: value.Kind(), typ: value.Type(), pointer: uintptr(value.UnsafePointer())}
		if value.Kind() == reflect.Slice {
			id.length, id.capacity = value.Len(), value.Cap()
		}
		if previous, ok := seen[id]; ok {
			return previous
		}
		var out reflect.Value
		switch value.Kind() {
		case reflect.Pointer:
			out = reflect.New(value.Type().Elem())
			seen[id] = out
			out.Elem().Set(cloneAccountListValue(value.Elem(), seen))
		case reflect.Map:
			out = reflect.MakeMapWithSize(value.Type(), value.Len())
			seen[id] = out
			iterator := value.MapRange()
			for iterator.Next() {
				out.SetMapIndex(iterator.Key(), cloneAccountListValue(iterator.Value(), seen))
			}
		case reflect.Slice:
			out = reflect.MakeSlice(value.Type(), value.Len(), value.Len())
			seen[id] = out
			// Back references may point at a row in the top-level slice. Register
			// those identities before walking its group/account relation cycles.
			for i := 0; i < value.Len(); i++ {
				if value.Index(i).CanAddr() {
					pointer := value.Index(i).Addr()
					seen[accountListCloneIdentity{kind: reflect.Pointer, typ: pointer.Type(), pointer: pointer.Pointer()}] = out.Index(i).Addr()
				}
			}
			for i := 0; i < value.Len(); i++ {
				out.Index(i).Set(cloneAccountListValue(value.Index(i), seen))
			}
		}
		return out
	case reflect.Struct:
		// time.Time and other scalar library types have private representation
		// that must remain intact, including time zone/monotonic information.
		if value.Type() == reflect.TypeFor[time.Time]() {
			return value
		}
		out := reflect.New(value.Type()).Elem()
		if value.Type() != reflect.TypeFor[Account]() {
			out.Set(value)
		}
		for i := 0; i < value.NumField(); i++ {
			if value.Type().Field(i).IsExported() {
				out.Field(i).Set(cloneAccountListValue(value.Field(i), seen))
			}
		}
		return out
	case reflect.Array:
		out := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			out.Index(i).Set(cloneAccountListValue(value.Index(i), seen))
		}
		return out
	default:
		return value
	}
}

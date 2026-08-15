package rolesource

import (
	"fmt"
	"sort"
)

// DescriptorProvider is the control-plane view of the trusted adapter set.
// It intentionally has no Scan method: only a daemon-side Registry may read a
// source. The server uses a Catalog to negotiate kinds/versions safely.
type DescriptorProvider interface {
	Descriptor(Kind) (Descriptor, bool)
}

type Catalog struct {
	descriptors map[Kind]Descriptor
}

func NewCatalog(descriptors ...Descriptor) (*Catalog, error) {
	catalog := &Catalog{descriptors: make(map[Kind]Descriptor, len(descriptors))}
	for _, descriptor := range descriptors {
		if err := validateDescriptor(descriptor); err != nil {
			return nil, err
		}
		if _, exists := catalog.descriptors[descriptor.Kind]; exists {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateAdapter, descriptor.Kind)
		}
		catalog.descriptors[descriptor.Kind] = descriptor
	}
	return catalog, nil
}

func (c *Catalog) Descriptor(kind Kind) (Descriptor, bool) {
	if c == nil {
		return Descriptor{}, false
	}
	descriptor, ok := c.descriptors[kind]
	return descriptor, ok
}

func (c *Catalog) Descriptors() []Descriptor {
	if c == nil {
		return []Descriptor{}
	}
	result := make([]Descriptor, 0, len(c.descriptors))
	for _, descriptor := range c.descriptors {
		result = append(result, descriptor)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Kind < result[j].Kind })
	return result
}

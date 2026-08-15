package rolesource

import (
	"errors"
	"testing"
)

func TestCatalogIsDescriptorOnlyAndRejectsDuplicates(t *testing.T) {
	descriptor := validFakeAdapter("fake_source").Descriptor()
	catalog, err := NewCatalog(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := catalog.Descriptor("fake_source"); !ok || got != descriptor {
		t.Fatalf("catalog descriptor = %+v, %t", got, ok)
	}
	if _, err := NewCatalog(descriptor, descriptor); !errors.Is(err, ErrDuplicateAdapter) {
		t.Fatalf("duplicate catalog error = %v", err)
	}
}

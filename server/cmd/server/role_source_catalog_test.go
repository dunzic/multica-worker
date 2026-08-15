package main

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/rolesource/agentwaker"
	"github.com/multica-ai/multica/server/internal/rolesource/manifestdir"
	"github.com/multica-ai/multica/server/internal/rolesource/signedremote"
)

func TestRoleSourceCatalogPublishesAllDaemonAdapters(t *testing.T) {
	catalog, err := newRoleSourceAdapterCatalog()
	if err != nil {
		t.Fatal(err)
	}
	descriptors := catalog.Descriptors()
	if len(descriptors) != 3 || descriptors[0].Kind != agentwaker.Kind ||
		descriptors[1].Kind != manifestdir.Kind || descriptors[2].Kind != signedremote.Kind {
		t.Fatalf("role-source catalog=%+v", descriptors)
	}
}

func TestRoleSourceMaterializationConcurrencyFromEnv(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int
		valid bool
	}{
		{value: "", want: 2, valid: true},
		{value: "1", want: 1, valid: true},
		{value: "8", want: 8, valid: true},
		{value: "0"},
		{value: "9"},
		{value: "two"},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv("MULTICA_ROLE_SOURCE_APPLY_CONCURRENCY", test.value)
			got, err := roleSourceMaterializationConcurrencyFromEnv()
			if test.valid && (err != nil || got != test.want) {
				t.Fatalf("concurrency=%d err=%v, want %d", got, err, test.want)
			}
			if !test.valid && err == nil {
				t.Fatalf("invalid concurrency %q was accepted as %d", test.value, got)
			}
		})
	}
}

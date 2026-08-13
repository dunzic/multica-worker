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

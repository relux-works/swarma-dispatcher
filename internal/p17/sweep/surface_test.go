package sweep_test

import (
	"reflect"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

// Claim: export status can be reported, but the closed seam and both supplied implementations expose no home-copy, snapshot or preservation operation.
func TestExportEvidenceSurface(t *testing.T) {
	status, err := (sweep.StaticWorkProductExport{"run": sweep.Exported}).ExportStatus("run")
	if err != nil || status != sweep.Exported {
		t.Fatalf("positive evidence control: %q, %v", status, err)
	}
	for _, typ := range []reflect.Type{
		reflect.TypeFor[sweep.WorkProductExport](),
		reflect.TypeFor[sweep.UnpinnedWorkProductExport](),
		reflect.TypeFor[*sweep.UnpinnedWorkProductExport](),
		reflect.TypeFor[sweep.StaticWorkProductExport](),
		reflect.TypeFor[*sweep.StaticWorkProductExport](),
	} {
		t.Run(typ.String(), func(t *testing.T) {
			if typ.NumMethod() != 1 || typ.Method(0).Name != "ExportStatus" {
				t.Fatalf("report-only surface widened: %v has %d methods", typ, typ.NumMethod())
			}
			// These attempts cannot resolve through the API; there is no handler to invoke.
			for _, operation := range []string{"CopyHome", "SnapshotHome", "ArchiveHome"} {
				if _, accepted := typ.MethodByName(operation); accepted {
					t.Fatalf("home operation %s accepted by %v", operation, typ)
				}
			}
		})
	}
}

package extensions

import (
	"context"
	"errors"
	"testing"
)

func TestSourceInfoString(t *testing.T) {
	cases := []struct {
		source SourceInfo
		want   string
	}{
		{SourceInfo{Kind: SourceBuiltin}, "builtin"},
		{SourceInfo{Kind: SourcePackage, ID: "acme-tools"}, "package:acme-tools"},
		{SourceInfo{Kind: SourceProject, Path: ".cortex/extensions/x.go"}, "project:.cortex/extensions/x.go"},
		{SourceInfo{Kind: SourcePlugin, ID: "plug", Path: "/p"}, "plugin:plug"},
		{SourceInfo{}, "unknown"},
	}
	for _, tc := range cases {
		if got := tc.source.String(); got != tc.want {
			t.Fatalf("%+v.String() = %q, want %q", tc.source, got, tc.want)
		}
	}
}

func TestRegisterExtensionValidation(t *testing.T) {
	reg := NewRegistry()
	if err := reg.RegisterExtension(context.Background(), nil, SourceInfo{}, nil); !errors.Is(err, ErrNilExtension) {
		t.Fatalf("nil extension err = %v, want ErrNilExtension", err)
	}
	if err := reg.RegisterExtension(context.Background(), &stubExtension{}, SourceInfo{}, nil); !errors.Is(err, ErrEmptyName) {
		t.Fatalf("empty name err = %v, want ErrEmptyName", err)
	}
	if err := reg.UnregisterExtension(context.Background(), "missing"); !errors.Is(err, ErrExtensionNotFound) {
		t.Fatalf("missing extension err = %v, want ErrExtensionNotFound", err)
	}
}

func TestRegisterExtensionDefaultsSourceAndCapabilities(t *testing.T) {
	reg := NewRegistry()
	var nilCtxUsed bool
	ext := &stubExtension{
		name: "defaults",
		initFn: func(ctx context.Context, api *API) error {
			nilCtxUsed = ctx != nil
			if api.Tools == nil || api.Commands == nil || api.Models == nil ||
				api.Resources == nil || api.Events == nil || api.Sessions == nil || api.UI == nil {
				t.Fatalf("API has a nil capability: %+v", api)
			}
			if _, ok := api.Sessions.Current(); ok {
				t.Fatal("default session access should deny")
			}
			if got := api.Sessions.List(); got != nil {
				t.Fatalf("Sessions.List = %v, want nil", got)
			}
			api.UI.Notify(ctx, NotifyInfo, "hello")
			if _, err := api.UI.Confirm(ctx, "title", "message"); !errors.Is(err, ErrUINotAvailable) {
				t.Fatalf("Confirm err = %v, want ErrUINotAvailable", err)
			}
			if _, err := api.UI.Input(ctx, "title", "placeholder"); !errors.Is(err, ErrUINotAvailable) {
				t.Fatalf("Input err = %v, want ErrUINotAvailable", err)
			}
			if _, err := api.UI.Select(ctx, "title", nil); !errors.Is(err, ErrUINotAvailable) {
				t.Fatalf("Select err = %v, want ErrUINotAvailable", err)
			}
			return nil
		},
	}

	// A nil context must be replaced with Background before Init runs.
	if err := reg.RegisterExtension(nil, ext, SourceInfo{}, nil); err != nil {
		t.Fatalf("RegisterExtension: %v", err)
	}
	if !nilCtxUsed {
		t.Fatal("Init received a nil context")
	}
	snapshot := reg.Snapshot()
	if len(snapshot.Extensions) != 1 || snapshot.Extensions[0].Source.Kind != SourceUser {
		t.Fatalf("extensions = %+v, want default source user", snapshot.Extensions)
	}

	// Unregistration with a nil context must also work.
	if err := reg.UnregisterExtension(nil, "defaults"); err != nil {
		t.Fatalf("UnregisterExtension: %v", err)
	}
}

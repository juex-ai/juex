package eventcatalog

import (
	"sync"

	goalmodule "github.com/juex-ai/juex/internal/features/goal"
	notesmodule "github.com/juex-ai/juex/internal/features/notes"
	observable "github.com/juex-ai/juex/internal/features/observables"
	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/toolevents"
	"github.com/juex-ai/juex/internal/framework/provenance"
	juexruntime "github.com/juex-ai/juex/internal/framework/runtime"
	"github.com/juex-ai/juex/internal/framework/thread"
)

var (
	defaultOnce    sync.Once
	defaultCatalog *events.Catalog
)

func Default() *events.Catalog {
	defaultOnce.Do(func() {
		var err error
		defaultCatalog, err = events.NewCatalog(builtinDefinitions()...)
		if err != nil {
			panic(err)
		}
	})
	return defaultCatalog
}

func builtinDefinitions() []events.Definition {
	var definitions []events.Definition
	runtimeDefinitions := juexruntime.EventDefinitions()
	// Browser subscriptions retain their established order across schema owners.
	appendRuntimeThrough := func(eventType string) {
		for len(runtimeDefinitions) > 0 {
			definition := runtimeDefinitions[0]
			runtimeDefinitions = runtimeDefinitions[1:]
			definitions = append(definitions, definition)
			if definition.Type == eventType {
				return
			}
		}
		panic("event catalog: missing runtime schema " + eventType)
	}
	appendRuntimeThrough("llm.errored")
	definitions = append(definitions, provenance.EventDefinitions()...)
	appendRuntimeThrough("llm.fallback")
	definitions = append(definitions, toolevents.EventDefinitions()...)
	appendRuntimeThrough("pending_input.rejected")
	definitions = append(definitions, goalmodule.EventDefinitions()...)
	definitions = append(definitions, notesmodule.EventDefinitions()...)
	definitions = append(definitions, observable.EventDefinitions()...)
	definitions = append(definitions, runtimeDefinitions...)
	definitions = append(definitions, thread.EventDefinitions()...)
	return definitions
}

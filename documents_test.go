package engineer

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pacenote-sim/protocol/wire"
)

// document is a value encoded the way the host hands it over.
func document(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}

func TestTheSetupDocumentIsReadOrLeftAlone(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	setup := setupOf(document(t, wire.CarSetup{Tyres: []wire.SetupTyre{{Wheel: wire.WheelLF, ColdKpa: 165}}}))
	r.NotNil(setup)
	r.Equal(wire.WheelLF, setup.Tyres[0].Wheel)

	// A field this plugin does not know is ignored, not fatal: the client
	// sending something new must not break the coach that predates it.
	r.NotNil(setupOf(json.RawMessage(`{"update_count":1,"something_new":true}`)))

	// Nothing, and a document that is not a setup, are both no setup.
	r.Nil(setupOf(nil))
	r.Nil(setupOf(json.RawMessage(`"not an object"`)))
}

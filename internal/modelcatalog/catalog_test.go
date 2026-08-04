package modelcatalog

import (
	"encoding/json"
	"testing"
)

func TestDecodeCatalogRejectsTrailingJSONValue(t *testing.T) {
	contents, err := json.Marshal(catalogFixture("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, []byte(`{}`)...)

	if _, err := decodeCatalog(contents); err == nil {
		t.Fatal("decodeCatalog accepted a trailing JSON value")
	}
}

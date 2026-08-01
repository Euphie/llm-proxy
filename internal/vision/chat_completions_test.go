package vision

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestChatCompletionsDocumentRewritesImagesAndPreservesRequest(t *testing.T) {
	body := []byte(`{
		"model":"text-model",
		"stream":true,
		"vendor_extension":{"enabled":true},
		"messages":[
			{"role":"system","content":"be helpful"},
			{"role":"user","content":[
				{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U=","detail":"high"}},
				{"type":"text","text":"这个界面哪里有问题？"}
			]}
		]
	}`)
	doc, err := parseChatCompletions(body)
	if err != nil {
		t.Fatal(err)
	}
	images := doc.images()
	if len(images) != 1 || images[0].sourceType != "data_url" ||
		images[0].detail != "high" || !strings.Contains(images[0].taskContext, "界面") {
		t.Fatalf("images=%+v", images)
	}

	rewritten, err := doc.rewrite([]string{"按钮与输入框重叠"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rewritten, []byte(`"type":"image_url"`)) ||
		!bytes.Contains(rewritten, []byte("按钮与输入框重叠")) {
		t.Fatalf("rewritten=%s", rewritten)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(rewritten, &root); err != nil {
		t.Fatal(err)
	}
	if string(root["stream"]) != "true" || string(root["vendor_extension"]) != `{"enabled":true}` {
		t.Fatalf("root=%s", rewritten)
	}
}

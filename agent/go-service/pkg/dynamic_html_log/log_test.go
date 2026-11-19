package dynamic_html_log

import "testing"

func TestShouldPrint(t *testing.T) {
	cases := []struct {
		name string
		log  *DynamicHtmlLog
		msg  string
		want bool
	}{
		{
			name: "plain-text client suppresses the whole countdown",
			log:  &DynamicHtmlLog{plainText: true},
			msg:  "请稍后 10秒 ...",
			want: false,
		},
		{
			name: "css client prints a new message",
			log:  &DynamicHtmlLog{plainText: false},
			msg:  "请稍后 10秒 ...",
			want: true,
		},
		{
			name: "duplicate message skipped",
			log:  &DynamicHtmlLog{plainText: false, lastMessage: "请稍后 10秒 ..."},
			msg:  "请稍后 10秒 ...",
			want: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := &DynamicHtmlLogData{log: c.log, message: c.msg}
			if got := data.shouldPrint(); got != c.want {
				t.Fatalf("shouldPrint() = %v, want %v", got, c.want)
			}
		})
	}
}

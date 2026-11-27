package ado

import (
	"reflect"
	"testing"
)

func TestHTMLBlocks(t *testing.T) {
	for _, test := range []struct {
		input string
		want  []block
	}{
		{`<p>&quot;pat&#39;s&quot; &lt;script&gt; &amp;lt;</p>`, []block{{paragraph, `"pat's" <script> &lt;`}}},
		{`<div>one&nbsp; two<br>Repo: acme/app</div><p>three</p>`, []block{{paragraph, "one two"}, {paragraph, "Repo: acme/app"}, {paragraph, "three"}}},
		{`<ul><li><b>first</b><ol><li><a href="x>y">second</a></li></ol></li><li>third</li></ul>`, []block{{item, "first"}, {item, "second"}, {item, "third"}}},
		{`<h2>heading</h2><p>one<!-- hidden --> two</p>`, []block{{heading, "heading"}, {paragraph, "one two"}}},
		{`<p>text <broken`, []block{{paragraph, "text <broken"}}},
		{`<p>a < b</p><p>next</p>`, []block{{paragraph, "a < b"}, {paragraph, "next"}}},
		{`<p>a<b</p>`, []block{{paragraph, "a<b"}}},
		{`<p>a<b attr</p><p>next</p>`, []block{{paragraph, "a<b attr"}, {paragraph, "next"}}},
		{`a < b and c > d`, []block{{paragraph, "a < b and c > d"}}},
		{"Check x < y\nRepo: acme/app", []block{{paragraph, "Check x < y"}, {paragraph, "Repo: acme/app"}}},
		{`<P><custom-tag2 title="x<y>z">text</custom-tag2><br/>next</P>`, []block{{paragraph, "text"}, {paragraph, "next"}}},
		{"a <b.c> d\nnext", []block{{paragraph, "a <b.c> d"}, {paragraph, "next"}}},
		{"text <!-- unclosed\nnext", []block{{paragraph, "text <!-- unclosed"}, {paragraph, "next"}}},
		{"one\n\n two", []block{{paragraph, "one"}, {paragraph, "two"}}},
		{"", nil},
	} {
		if got := htmlBlocks(test.input); !reflect.DeepEqual(got, test.want) {
			t.Errorf("%q: got %#v, want %#v", test.input, got, test.want)
		}
	}
}

package schemas

import "testing"

func TestCanonicalEntitySet(t *testing.T) {
	cases := []struct {
		name              string
		ids, names        []string
		wantIDs, wantName string
	}{
		{"empty", nil, nil, "", ""},
		{"single", []string{"a1"}, []string{"Alpha"}, "a1", "Alpha"},
		{
			"sorted and aligned",
			[]string{"c3", "a1", "b2"},
			[]string{"Gamma", "Alpha", "Beta"},
			"a1,b2,c3", "Alpha,Beta,Gamma",
		},
		{
			"deterministic regardless of input order",
			[]string{"b2", "c3", "a1"},
			[]string{"Beta", "Gamma", "Alpha"},
			"a1,b2,c3", "Alpha,Beta,Gamma",
		},
		{
			"dedupe by id keeps first name",
			[]string{"a1", "a1", "b2"},
			[]string{"Alpha", "AlphaDup", "Beta"},
			"a1,b2", "Alpha,Beta",
		},
		{"drops empty ids", []string{"", "a1"}, []string{"", "Alpha"}, "a1", "Alpha"},
		{"missing names tolerated", []string{"a1", "b2"}, []string{"Alpha"}, "a1,b2", "Alpha,"},
	}
	for _, c := range cases {
		gotIDs, gotNames := CanonicalEntitySet(c.ids, c.names)
		if gotIDs != c.wantIDs || gotNames != c.wantName {
			t.Errorf("%s: CanonicalEntitySet(%v,%v) = (%q,%q), want (%q,%q)",
				c.name, c.ids, c.names, gotIDs, gotNames, c.wantIDs, c.wantName)
		}
	}
}

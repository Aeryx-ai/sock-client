package client

import "testing"

func TestMessageOwnerEnvThenFileThenDefault(t *testing.T) {
	on, off := true, false
	cases := []struct {
		env  string
		file *bool
		want bool
		from string
	}{
		{"", nil, true, MessageOwnerDefault},
		{"", &off, false, MessageOwnerFile},
		{"", &on, true, MessageOwnerFile},
		{"1", &off, true, MessageOwnerEnv},
		{"true", &off, true, MessageOwnerEnv},
		{"0", &on, false, MessageOwnerEnv},
		{"false", nil, false, MessageOwnerEnv},
		{"on", &off, true, MessageOwnerEnv},
		{"off", &on, false, MessageOwnerEnv},
		// any other value set is not a setting: it fails closed, never to the
		// file or the default (an operator who wrote "no" meant off)
		{"no", &on, false, MessageOwnerEnv},
		{"TRUE", nil, false, MessageOwnerEnv},
		{" false", nil, false, MessageOwnerEnv},
		{"${user_config.message_owner}", &on, false, MessageOwnerEnv},
	}
	for _, c := range cases {
		getenv := func(k string) string {
			if k == MessageOwnerEnvVar {
				return c.env
			}
			return ""
		}
		got, from := MessageOwner(getenv, FileConfig{MessageOwner: c.file})
		if got != c.want || from != c.from {
			t.Errorf("env %q file %v: got %v from %s, want %v from %s", c.env, c.file, got, from, c.want, c.from)
		}
	}
}

func TestLoadFileConfigReadsMessageOwner(t *testing.T) {
	p := writeConfig(t, 0o600, "message_owner = false\n")
	f, err := LoadFileConfig(p)
	if err != nil || f.MessageOwner == nil || *f.MessageOwner {
		t.Fatalf("got %+v, %v", f, err)
	}
}

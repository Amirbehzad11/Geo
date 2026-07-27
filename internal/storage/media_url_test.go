package storage

import "testing"

func TestResolvePublicMediaURL(t *testing.T) {
	cases := []struct {
		base, path, want string
	}{
		{"http://192.168.20.142", "vehicles/car.png", "http://192.168.20.142/storage/vehicles/car.png"},
		{"http://192.168.20.142/", "vehicles/car.png", "http://192.168.20.142/storage/vehicles/car.png"},
		{"http://192.168.20.142/storage", "vehicles/car.png", "http://192.168.20.142/storage/vehicles/car.png"},
		{"http://192.168.20.142/storage/", "storage/vehicles/car.png", "http://192.168.20.142/storage/vehicles/car.png"},
		{"http://host", "https://cdn.example/a.png", "https://cdn.example/a.png"},
		{"http://host", "", ""},
		{"", "vehicles/car.png", "vehicles/car.png"},
		{"http://host", "/storage/vehicles/car.png", "http://host/storage/vehicles/car.png"},
	}
	for _, tc := range cases {
		got := ResolvePublicMediaURL(tc.base, tc.path)
		if got != tc.want {
			t.Fatalf("ResolvePublicMediaURL(%q,%q)=%q want %q", tc.base, tc.path, got, tc.want)
		}
	}
}

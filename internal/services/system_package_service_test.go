package services

import "testing"

func TestSafePackageRemovalOnlyVersionedPHPPackages(t *testing.T) {
	cases := []struct {
		manager string
		pkg     string
		want    bool
	}{
		{manager: "apt", pkg: "php8.3-fpm", want: true},
		{manager: "apt", pkg: "php8.4-cli", want: true},
		{manager: "apt", pkg: "php-fpm", want: false},
		{manager: "apt", pkg: "nginx", want: false},
		{manager: "dnf", pkg: "php83-php-fpm", want: true},
		{manager: "yum", pkg: "php83-php-cli", want: true},
		{manager: "dnf", pkg: "php-fpm", want: false},
		{manager: "zypper", pkg: "php-fpm", want: false},
	}
	for _, tc := range cases {
		if got := safePackageRemoval(tc.manager, tc.pkg); got != tc.want {
			t.Fatalf("safePackageRemoval(%q, %q) = %v, want %v", tc.manager, tc.pkg, got, tc.want)
		}
	}
}

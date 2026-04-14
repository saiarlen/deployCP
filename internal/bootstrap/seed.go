package bootstrap

// Seed initializes the application stack so defaults and bootstrap data are created.
func Seed() error {
	_, err := Build()
	return err
}

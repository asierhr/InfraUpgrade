package migration

func DefaultEngine() Engine {
	return Engine{
		Rules: []Rule{},
	}
}

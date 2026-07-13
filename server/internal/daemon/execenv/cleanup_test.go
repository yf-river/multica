package execenv

import "os"

func (env *Environment) Cleanup(removeAll bool) error {
	if env == nil {
		return nil
	}
	if env.LocalDirectory {
		if removeAll && env.RootDir != "" {
			return os.RemoveAll(env.RootDir)
		}
		return nil
	}
	if removeAll {
		return os.RemoveAll(env.RootDir)
	}
	return os.RemoveAll(env.WorkDir)
}

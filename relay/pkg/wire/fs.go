package wire

// FSStatParams are fs.stat's: is there something at Path on Machine
// (empty or this machine's short name: here)? The app checks a draft's
// folder on the machine it will run on before starting it. Path is a
// clean absolute path.
type FSStatParams struct {
	Machine string `json:"machine,omitempty"`
	Path    string `json:"path"`
}

// FSStat is fs.stat's result. Nothing is revealed but these two bits.
type FSStat struct {
	Exists bool `json:"exists"`
	IsDir  bool `json:"isDir"`
}

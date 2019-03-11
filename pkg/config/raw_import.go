package config

type rawImport struct {
	ImageName    string `yaml:"image,omitempty"`
	ArtifactName string `yaml:"artifact,omitempty"`
	Before       string `yaml:"before,omitempty"`
	After        string `yaml:"after,omitempty"`

	rawArtifactExport `yaml:",inline"`
	rawImage          *rawImage `yaml:"-"` // parent

	UnsupportedAttributes map[string]interface{} `yaml:",inline"`
}

func (c *rawImport) configSection() interface{} {
	return c
}

func (c *rawImport) doc() *doc {
	return c.rawImage.doc
}

func (c *rawImport) UnmarshalYAML(unmarshal func(interface{}) error) error {
	if parent, ok := parentStack.Peek().(*rawImage); ok {
		c.rawImage = parent
	}

	parentStack.Push(c)
	type plain rawImport
	err := unmarshal((*plain)(c))
	parentStack.Pop()
	if err != nil {
		return err
	}

	c.rawArtifactExport.inlinedIntoRaw(c)

	if err := checkOverflow(c.UnsupportedAttributes, c, c.rawImage.doc); err != nil {
		return err
	}

	if c.rawArtifactExport.rawExportBase.To == "" {
		c.rawArtifactExport.rawExportBase.To = c.rawArtifactExport.rawExportBase.Add
	}

	return nil
}

func (c *rawImport) toDirective() (importInstance *Import, err error) {
	importInstance = &Import{}

	if artifactExport, err := c.rawArtifactExport.toDirective(); err != nil {
		return nil, err
	} else {
		importInstance.ArtifactExport = artifactExport
	}

	importInstance.ImageName = c.ImageName
	importInstance.ArtifactName = c.ArtifactName
	importInstance.Before = c.Before
	importInstance.After = c.After

	importInstance.raw = c

	if err = c.validateDirective(importInstance); err != nil {
		return nil, err
	}

	return importInstance, nil
}

func (c *rawImport) validateDirective(importInstance *Import) (err error) {
	if err = importInstance.validate(); err != nil {
		return err
	}

	return nil
}
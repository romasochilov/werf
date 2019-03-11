package stage

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/flant/werf/pkg/config"
	"github.com/flant/werf/pkg/dappdeps"
	"github.com/flant/werf/pkg/docker"
	imagePkg "github.com/flant/werf/pkg/image"
	"github.com/flant/werf/pkg/slug"
	"github.com/flant/werf/pkg/util"
)

type getImportsOptions struct {
	Before StageName
	After  StageName
}

func getImports(imageBaseConfig *config.ImageBase, options *getImportsOptions) []*config.Import {
	var imports []*config.Import
	for _, elm := range imageBaseConfig.Import {
		if options.Before != "" && elm.Before != "" && elm.Before == string(options.Before) {
			imports = append(imports, elm)
		} else if options.After != "" && elm.After != "" && elm.After == string(options.After) {
			imports = append(imports, elm)
		}
	}

	return imports
}

func newImportStage(imports []*config.Import, name StageName, baseStageOptions *NewBaseStageOptions) *ImportStage {
	s := &ImportStage{}
	s.imports = imports
	s.BaseStage = newBaseStage(name, baseStageOptions)
	return s
}

type ImportStage struct {
	*BaseStage

	imports []*config.Import
}

func (s *ImportStage) GetDependencies(c Conveyor, _ imagePkg.ImageInterface) (string, error) {
	var args []string

	for _, elm := range s.imports {
		if elm.ImageName != "" {
			args = append(args, c.GetImageLatestStageSignature(elm.ImageName))
		} else {
			args = append(args, c.GetImageLatestStageSignature(elm.ArtifactName))
		}

		args = append(args, elm.Add, elm.To)
		args = append(args, elm.Group, elm.Owner)
		args = append(args, elm.IncludePaths...)
		args = append(args, elm.ExcludePaths...)
	}

	return util.Sha256Hash(args...), nil
}

func (s *ImportStage) PrepareImage(c Conveyor, _, image imagePkg.ImageInterface) error {
	for _, elm := range s.imports {
		importFromContainerTmpPath := s.importContainerTmpPath(elm)
		command := generateSafeCp(importFromContainerTmpPath, elm.To, elm.Owner, elm.Group, elm.IncludePaths, elm.ExcludePaths)
		importImageTmpDir, importImageContainerTmpDir := s.importImageTmpDirs(elm)
		volume := fmt.Sprintf("%s:%s:ro", importImageTmpDir, importImageContainerTmpDir)

		image.Container().AddServiceRunCommands(command)
		image.Container().RunOptions().AddVolume(volume)

		imageServiceCommitChangeOptions := image.Container().ServiceCommitChangeOptions()

		var labelKey, labelValue string
		if elm.ImageName != "" {
			labelKey = fmt.Sprintf("%s%s", imagePkg.WerfImportMountLabelPrefix, slug.Slug(elm.ImageName))
			labelValue = c.GetImageLatestStageSignature(elm.ImageName)
		} else {
			labelKey = fmt.Sprintf("%s%s", imagePkg.WerfImportMountLabelPrefix, slug.Slug(elm.ArtifactName))
			labelValue = c.GetImageLatestStageSignature(elm.ArtifactName)
		}

		imageServiceCommitChangeOptions.AddLabel(map[string]string{labelKey: labelValue})
	}

	return nil
}

func (s *ImportStage) PreRunHook(c Conveyor) error {
	for _, elm := range s.imports {
		if err := s.prepareImportData(c, elm); err != nil {
			return err
		}
	}

	return nil
}

func (s *ImportStage) prepareImportData(c Conveyor, i *config.Import) error {
	importContainerTmpPath := s.importContainerTmpPath(i)

	imageCommand := generateSafeCp(i.Add, importContainerTmpPath, "", "", []string{}, []string{})

	toolchainContainer, err := dappdeps.ToolchainContainer()
	if err != nil {
		return err
	}

	baseContainer, err := dappdeps.BaseContainer()
	if err != nil {
		return err
	}

	importImageTmp, importImageContainerTmp := s.importImageTmpDirs(i)

	var dockerImageName string
	if i.ImageName != "" {
		dockerImageName = c.GetImageLatestStageImageName(i.ImageName)
	} else {
		dockerImageName = c.GetImageLatestStageImageName(i.ArtifactName)
	}

	args := []string{
		"--rm",
		fmt.Sprintf("--volumes-from=%s", toolchainContainer),
		fmt.Sprintf("--volumes-from=%s", baseContainer),
		fmt.Sprintf("--entrypoint=%s", dappdeps.BaseBinPath("bash")),
		fmt.Sprintf("--volume=%s:%s", importImageTmp, importImageContainerTmp),
		dockerImageName,
		"-ec",
		imagePkg.ShelloutPack(imageCommand),
	}

	err = docker.CliRun(args...)
	if err != nil {
		return err
	}

	return nil
}

func (s *ImportStage) importContainerTmpPath(i *config.Import) string {
	importID := util.Sha256Hash(fmt.Sprintf("%+v", i))
	_, importImageContainerTmpPath := s.importImageTmpDirs(i)
	importContainerTmpPath := path.Join(importImageContainerTmpPath, importID)

	return importContainerTmpPath
}

func (s *ImportStage) importImageTmpDirs(i *config.Import) (string, string) {
	var importNamePathPart string
	if i.ImageName != "" {
		importNamePathPart = slug.Slug(i.ImageName)
	} else {
		importNamePathPart = slug.Slug(i.ArtifactName)
	}

	importImageTmpDir := path.Join(s.imageTmpDir, "import", importNamePathPart)
	importImageContainerTmpDir := path.Join(s.containerWerfDir, "import", importNamePathPart)

	return importImageTmpDir, importImageContainerTmpDir
}

func generateSafeCp(from, to, owner, group string, includePaths, excludePaths []string) string {
	var args []string

	mkdirBin := dappdeps.BaseBinPath("mkdir")
	mkdirPath := path.Dir(to)
	mkdirCommand := fmt.Sprintf("%s -p %s", mkdirBin, mkdirPath)

	rsyncBin := dappdeps.BaseBinPath("rsync")
	var rsyncChownOption string
	if owner != "" || group != "" {
		rsyncChownOption = fmt.Sprintf("--chown=%s:%s", owner, group)
	}
	rsyncCommand := fmt.Sprintf("%s --archive --links --inplace %s", rsyncBin, rsyncChownOption)

	if len(includePaths) != 0 {
		/**
				Если указали include_paths — это означает, что надо копировать
				только указанные пути. Поэтому exclude_paths в приоритете, т.к. в данном режиме
		        exclude_paths может относится только к путям, указанным в include_paths.
		        При этом случай, когда в include_paths указали более специальный путь, чем в exclude_paths,
		        будет обрабатываться в пользу exclude, этот путь не скопируется.
		*/
		for _, p := range excludePaths {
			rsyncCommand += fmt.Sprintf(" --filter='-/ %s'", path.Join(from, p))
		}

		for _, p := range includePaths {
			targetPath := path.Join(from, p)

			// Генерируем разрешающее правило для каждого элемента пути
			for _, pathPart := range descentPath(targetPath) {
				rsyncCommand += fmt.Sprintf(" --filter='+/ %s'", pathPart)
			}

			/**
					На данный момент не знаем директорию или файл имел в виду пользователь,
			        поэтому подставляем фильтры для обоих возможных случаев.

					Автоматом подставляем паттерн ** для включения файлов, содержащихся в
			        директории, которую пользователь указал в include_paths.
			*/
			rsyncCommand += fmt.Sprintf(" --filter='+/ %s'", targetPath)
			rsyncCommand += fmt.Sprintf(" --filter='+/ %s'", path.Join(targetPath, "**"))
		}

		// Все что не подошло по include — исключается
		rsyncCommand += fmt.Sprintf(" --filter='-/ %s'", path.Join(from, "**"))
	} else {
		for _, p := range excludePaths {
			rsyncCommand += fmt.Sprintf(" --filter='-/ %s'", path.Join(from, p))
		}
	}

	/**
		Слэш после from — это инструкция rsync'у для копирования
	    содержимого директории from, а не самой директории.
	*/
	rsyncCommand += fmt.Sprintf(" $(if [ -d %[1]s ] ; then echo %[1]s/ ; else echo %[1]s ; fi) %[2]s", from, to)

	args = append(args, mkdirCommand, rsyncCommand)
	command := strings.Join(args, " && ")

	return command
}

func descentPath(filePath string) []string {
	var parts []string

	part := filePath
	for {
		parts = append(parts, part)
		part = path.Dir(part)

		if part == path.Dir(part) {
			break
		}
	}

	sort.Sort(sort.Reverse(sort.StringSlice(parts[:])))

	return parts
}
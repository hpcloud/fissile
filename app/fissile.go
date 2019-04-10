package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hpcloud/fissile/builder"
	"github.com/hpcloud/fissile/compilator"
	"github.com/hpcloud/fissile/docker"
	"github.com/hpcloud/fissile/kube"
	"github.com/hpcloud/fissile/model"
	"github.com/hpcloud/fissile/scripts/compilation"
	"github.com/hpcloud/fissile/util"

	"github.com/fatih/color"
	"github.com/hpcloud/stampy"
	"github.com/hpcloud/termui"
	"github.com/joho/godotenv"
	"gopkg.in/yaml.v2"
)

// Fissile represents a fissile application
type Fissile struct {
	Version                    string
	UI                         *termui.UI
	cmdErr                     error
	releases                   []*model.Release // Only applies for some commands
	patchPropertiesReleaseName string           // Only applies for some commands
	patchPropertiesJobName     string           // Only applies for some commands
}

// NewFissileApplication creates a new app.Fissile
func NewFissileApplication(version string, ui *termui.UI) *Fissile {
	return &Fissile{
		Version: version,
		UI:      ui,
	}
}

// SetPatchPropertiesDirective saves the patch-properties release and job names, if specified.
func (f *Fissile) SetPatchPropertiesDirective(patchPropertiesDirective string) error {
	if patchPropertiesDirective == "" {
		return nil
	}
	msgStart := "Invalid format for --patch-properties-release flag: should be RELEASE/JOB;"
	parts := strings.Split(patchPropertiesDirective, "/")
	if len(parts) != 2 {
		return fmt.Errorf(msgStart+" got %d part(s)", len(parts))
	}
	if parts[0] == "" {
		return fmt.Errorf(msgStart + " no RELEASE is specified")
	}
	if parts[1] == "" {
		return fmt.Errorf(msgStart + " no JOB is specified")
	}
	f.patchPropertiesReleaseName = parts[0]
	f.patchPropertiesJobName = parts[1]
	return nil
}

// ShowBaseImage will show details about the base BOSH images
func (f *Fissile) ShowBaseImage(repository string) error {
	dockerManager, err := docker.NewImageManager()
	if err != nil {
		return fmt.Errorf("Error connecting to docker: %s", err.Error())
	}

	comp, err := compilator.NewCompilator(dockerManager, "", "", repository, compilation.UbuntuBase, f.Version, false, f.UI)
	if err != nil {
		return fmt.Errorf("Error creating a new compilator: %s", err.Error())
	}

	image, err := dockerManager.FindImage(comp.BaseImageName())
	if err != nil {
		return fmt.Errorf("Error looking up base image %s: %s", comp.BaseImageName(), err.Error())
	}

	f.UI.Printf("\nCompilation Layer: %s\n", color.GreenString(comp.BaseImageName()))
	f.UI.Printf("ID: %s\n", color.GreenString(image.ID))
	f.UI.Printf("Virtual Size: %sMB\n", color.YellowString("%.2f", float64(image.VirtualSize)/(1024*1024)))

	baseImageName := builder.GetBaseImageName(repository, f.Version)
	image, err = dockerManager.FindImage(baseImageName)
	f.UI.Printf("\nStemcell Layer: %s\n", color.GreenString(baseImageName))
	f.UI.Printf("ID: %s\n", color.GreenString(image.ID))
	f.UI.Printf("Virtual Size: %sMB\n", color.YellowString("%.2f", float64(image.VirtualSize)/(1024*1024)))

	return nil
}

// CreateBaseCompilationImage will recompile the base BOSH image for a release
func (f *Fissile) CreateBaseCompilationImage(baseImageName, repository, metricsPath string, keepContainer bool) error {
	if metricsPath != "" {
		stampy.Stamp(metricsPath, "fissile", "create-compilation-image", "start")
		defer stampy.Stamp(metricsPath, "fissile", "create-compilation-image", "done")
	}

	dockerManager, err := docker.NewImageManager()
	if err != nil {
		return fmt.Errorf("Error connecting to docker: %s", err.Error())
	}

	baseImage, err := dockerManager.FindImage(baseImageName)
	if err != nil {
		return fmt.Errorf("Error looking up base image %s: %s", baseImageName, err)
	}

	f.UI.Println(color.GreenString("Base image with ID %s found", color.YellowString(baseImage.ID)))

	comp, err := compilator.NewCompilator(dockerManager, "", "", repository, compilation.UbuntuBase, f.Version, keepContainer, f.UI)
	if err != nil {
		return fmt.Errorf("Error creating a new compilator: %s", err.Error())
	}

	if _, err := comp.CreateCompilationBase(baseImageName); err != nil {
		return fmt.Errorf("Error creating compilation base image: %s", err.Error())
	}

	return nil
}

// GenerateBaseDockerImage generates a base docker image to be used as a FROM for role images
func (f *Fissile) GenerateBaseDockerImage(targetPath, baseImage, metricsPath string, noBuild bool, repository string) error {
	if metricsPath != "" {
		stampy.Stamp(metricsPath, "fissile", "create-role-base", "start")
		defer stampy.Stamp(metricsPath, "fissile", "create-role-base", "done")
	}

	dockerManager, err := docker.NewImageManager()
	if err != nil {
		return fmt.Errorf("Error connecting to docker: %s", err.Error())
	}

	baseImageName := builder.GetBaseImageName(repository, f.Version)

	image, err := dockerManager.FindImage(baseImageName)
	if err == docker.ErrImageNotFound {
		f.UI.Println("Image doesn't exist, it will be created ...")
	} else if err != nil {
		return fmt.Errorf("Error looking up image: %s", err.Error())
	} else {
		f.UI.Println(color.GreenString(
			"Base role image %s with ID %s already exists. Doing nothing.",
			color.YellowString(baseImageName),
			color.YellowString(image.ID),
		))
		return nil
	}

	if !strings.HasSuffix(targetPath, string(os.PathSeparator)) {
		targetPath = fmt.Sprintf("%s%c", targetPath, os.PathSeparator)
	}

	baseImageBuilder := builder.NewBaseImageBuilder(baseImage)

	if noBuild {
		f.UI.Println("Skipping image build because of flag.")
		return nil
	}

	f.UI.Println("Building base docker image ...")
	log := new(bytes.Buffer)
	stdoutWriter := docker.NewFormattingWriter(
		log,
		docker.ColoredBuildStringFunc(baseImageName),
	)

	tarPopulator := baseImageBuilder.NewDockerPopulator()
	err = dockerManager.BuildImageFromCallback(baseImageName, stdoutWriter, tarPopulator)
	if err != nil {
		log.WriteTo(f.UI)
		return fmt.Errorf("Error building base image: %s", err)
	}
	f.UI.Println(color.GreenString("Done."))

	return nil
}

// ListPackages will list all BOSH packages within a list of dev releases
func (f *Fissile) ListPackages() error {
	if len(f.releases) == 0 {
		return fmt.Errorf("Releases not loaded")
	}

	for _, release := range f.releases {
		f.UI.Println(color.GreenString("Dev release %s (%s)", color.YellowString(release.Name), color.MagentaString(release.Version)))

		for _, pkg := range release.Packages {
			f.UI.Printf("%s (%s)\n", color.YellowString(pkg.Name), color.WhiteString(pkg.Version))
		}

		f.UI.Printf(
			"There are %s packages present.\n\n",
			color.GreenString("%d", len(release.Packages)),
		)
	}

	return nil
}

// ListJobs will list all jobs within a list of dev releases
func (f *Fissile) ListJobs() error {
	if len(f.releases) == 0 {
		return fmt.Errorf("Releases not loaded")
	}

	for _, release := range f.releases {
		f.UI.Println(color.GreenString("Dev release %s (%s)", color.YellowString(release.Name), color.MagentaString(release.Version)))

		for _, job := range release.Jobs {
			f.UI.Printf("%s (%s): %s\n", color.YellowString(job.Name), color.WhiteString(job.Version), job.Description)
		}

		f.UI.Printf(
			"There are %s jobs present.\n\n",
			color.GreenString("%d", len(release.Jobs)),
		)
	}

	return nil
}

// ListProperties will list all properties in all jobs within a list of dev releases
func (f *Fissile) ListProperties(outputFormat string) error {
	if len(f.releases) == 0 {
		return fmt.Errorf("Releases not loaded")
	}

	switch outputFormat {
	case "human":
		f.listPropertiesForHuman()
	case "json":
		// Note: The encoding/json is unable to handle type
		// -- map[interface {}]interface {}
		// Such types can occur when the default value has sub-structure.

		buf, err := util.JSONMarshal(f.collectProperties())
		if err != nil {
			return err
		}

		f.UI.Printf("%s", buf)
	case "yaml":
		buf, err := yaml.Marshal(f.collectProperties())
		if err != nil {
			return err
		}

		f.UI.Printf("%s", buf)
	default:
		return fmt.Errorf("Invalid output format '%s', expected one of human, json, or yaml", outputFormat)
	}

	return nil
}

func (f *Fissile) listPropertiesForHuman() {
	// Human readable output.
	for _, release := range f.releases {
		f.UI.Println(color.GreenString("Dev release %s (%s)",
			color.YellowString(release.Name), color.MagentaString(release.Version)))

		for _, job := range release.Jobs {
			f.UI.Printf("%s (%s): %s\n", color.YellowString(job.Name),
				color.WhiteString(job.Version), job.Description)

			for _, property := range job.Properties {
				f.UI.Printf("\t%s: %v\n", color.YellowString(property.Name),
					property.Default)
			}
		}
	}
}

func (f *Fissile) collectProperties() map[string]map[string]map[string]interface{} {
	// Generate a triple map (release -> job -> property -> default value)
	// which is easy to convert and dump to JSON or YAML.

	result := make(map[string]map[string]map[string]interface{})

	for _, release := range f.releases {
		result[release.Name] = make(map[string]map[string]interface{})

		for _, job := range release.Jobs {
			result[release.Name][job.Name] = make(map[string]interface{})
			for _, property := range job.Properties {
				result[release.Name][job.Name][property.Name] = property.Default
			}
		}
	}

	return result
}

// Compile will compile a list of dev BOSH releases
func (f *Fissile) Compile(repository, targetPath, roleManifestPath, metricsPath string, workerCount int, skipDev bool) error {
	if len(f.releases) == 0 {
		return fmt.Errorf("Releases not loaded")
	}

	if metricsPath != "" {
		stampy.Stamp(metricsPath, "fissile", "compile-packages", "start")
		defer stampy.Stamp(metricsPath, "fissile", "compile-packages", "done")
	}

	dockerManager, err := docker.NewImageManager()
	if err != nil {
		return fmt.Errorf("Error connecting to docker: %s", err.Error())
	}

	roleManifest, err := model.LoadRoleManifest(roleManifestPath, f.releases, skipDev)
	if err != nil {
		return fmt.Errorf("Error loading roles manifest: %s", err.Error())
	}

	f.UI.Println(color.GreenString("Compiling packages for dev releases:"))
	for _, release := range f.releases {
		f.UI.Printf("         %s (%s)\n", color.YellowString(release.Name), color.MagentaString(release.Version))
	}

	comp, err := compilator.NewCompilator(dockerManager, targetPath, metricsPath, repository, compilation.UbuntuBase, f.Version, false, f.UI)
	if err != nil {
		return fmt.Errorf("Error creating a new compilator: %s", err.Error())
	}

	if err := comp.Compile(workerCount, f.releases, roleManifest); err != nil {
		return fmt.Errorf("Error compiling packages: %s", err.Error())
	}

	return nil
}

// CleanCache inspects the compilation cache and removes all packages
// which are not referenced (anymore).
func (f *Fissile) CleanCache(targetPath string) error {
	// 1. Collect list of packages referenced by the releases. A
	//    variant of the code in ListPackages, we keep only the
	//    hashes.

	if len(f.releases) == 0 {
		return fmt.Errorf("Releases not loaded")
	}

	referenced := make(map[string]int)
	for _, release := range f.releases {
		for _, pkg := range release.Packages {
			referenced[pkg.Version] = 1
		}
	}

	/// 2. Scan local compilation cache, compare to referenced,
	///    remove anything not found.

	f.UI.Printf("Cleaning up %s\n", color.MagentaString(targetPath))

	cached, err := filepath.Glob(targetPath + "/*")
	if err != nil {
		return err
	}

	removed := 0
	for _, cache := range cached {
		key := filepath.Base(cache)
		if _, ok := referenced[key]; ok {
			continue
		}

		f.UI.Printf("- Removing %s\n", color.YellowString(key))
		if err := os.RemoveAll(cache); err != nil {
			return err
		}
		removed++
	}

	if removed == 0 {
		f.UI.Println("Nothing found to remove")
		return nil
	}

	plural := ""
	if removed > 1 {
		plural = "s"
	}
	f.UI.Printf("Removed %s package%s\n",
		color.MagentaString(fmt.Sprintf("%d", removed)),
		plural)

	return nil
}

// GeneratePackagesRoleImage builds the docker image for the packages layer
// where all packages are included
func (f *Fissile) GeneratePackagesRoleImage(repository string, roleManifest *model.RoleManifest, noBuild, force bool, packagesImageBuilder *builder.PackagesImageBuilder) error {
	if len(f.releases) == 0 {
		return fmt.Errorf("Releases not loaded")
	}

	dockerManager, err := docker.NewImageManager()
	if err != nil {
		return fmt.Errorf("Error connecting to docker: %s", err.Error())
	}

	packagesLayerImageName := packagesImageBuilder.GetRolePackageImageName(roleManifest)
	if !force {
		if hasImage, err := dockerManager.HasImage(packagesLayerImageName); err == nil && hasImage {
			f.UI.Printf("Packages layer %s already exists. Skipping ...\n", color.YellowString(packagesLayerImageName))
			return nil
		}
	}

	baseImageName := builder.GetBaseImageName(repository, f.Version)
	if hasImage, err := dockerManager.HasImage(baseImageName); err != nil {
		return fmt.Errorf("Error getting base image: %s", err)
	} else if !hasImage {
		return fmt.Errorf("Failed to find role base %s, did you build it first?", baseImageName)
	}

	if noBuild {
		f.UI.Println("Skipping packages layer docker image build because of --no-build flag.")
		return nil
	}

	f.UI.Printf("Building packages layer docker image %s ...\n",
		color.YellowString(packagesLayerImageName))
	log := new(bytes.Buffer)
	stdoutWriter := docker.NewFormattingWriter(
		log,
		docker.ColoredBuildStringFunc(packagesLayerImageName),
	)

	tarPopulator := packagesImageBuilder.NewDockerPopulator(roleManifest, force)
	err = dockerManager.BuildImageFromCallback(packagesLayerImageName, stdoutWriter, tarPopulator)
	if err != nil {
		log.WriteTo(f.UI)
		return fmt.Errorf("Error building packages layer docker image: %s", err.Error())
	}
	f.UI.Println(color.GreenString("Done."))

	return nil
}

// GenerateRoleImages generates all role images using dev releases
func (f *Fissile) GenerateRoleImages(targetPath, repository, metricsPath string, noBuild, force bool, workerCount int, rolesManifestPath, compiledPackagesPath, lightManifestPath, darkManifestPath string, skipDev bool) error {
	if len(f.releases) == 0 {
		return fmt.Errorf("Releases not loaded")
	}

	if metricsPath != "" {
		stampy.Stamp(metricsPath, "fissile", "create-role-images", "start")
		defer stampy.Stamp(metricsPath, "fissile", "create-role-images", "done")
	}

	roleManifest, err := model.LoadRoleManifest(rolesManifestPath, f.releases, skipDev)
	if err != nil {
		return fmt.Errorf("Error loading roles manifest: %s", err.Error())
	}

	packagesImageBuilder, err := builder.NewPackagesImageBuilder(
		repository,
		compiledPackagesPath,
		targetPath,
		f.Version,
		f.UI,
	)
	if err != nil {
		return err
	}

	err = f.GeneratePackagesRoleImage(repository, roleManifest, noBuild, force, packagesImageBuilder)
	if err != nil {
		return err
	}

	packagesLayerImageName := packagesImageBuilder.GetRolePackageImageName(roleManifest)

	roleBuilder, err := builder.NewRoleImageBuilder(
		repository,
		compiledPackagesPath,
		targetPath,
		lightManifestPath,
		darkManifestPath,
		metricsPath,
		"",
		f.Version,
		f.UI,
	)
	if err != nil {
		return err
	}

	if err := roleBuilder.BuildRoleImages(roleManifest.Roles, repository, packagesLayerImageName, force, noBuild, workerCount); err != nil {
		return err
	}

	return nil
}

// ListRoleImages lists all dev role images
func (f *Fissile) ListRoleImages(repository string, rolesManifestPath string, existingOnDocker, withVirtualSize bool, skipDev bool) error {
	if withVirtualSize && !existingOnDocker {
		return fmt.Errorf("Cannot list image virtual sizes if not matching image names with docker")
	}

	if len(f.releases) == 0 {
		return fmt.Errorf("Releases not loaded")
	}

	var dockerManager *docker.ImageManager
	var err error

	if existingOnDocker {
		dockerManager, err = docker.NewImageManager()
		if err != nil {
			return fmt.Errorf("Error connecting to docker: %s", err.Error())
		}
	}

	rolesManifest, err := model.LoadRoleManifest(rolesManifestPath, f.releases, skipDev)
	if err != nil {
		return fmt.Errorf("Error loading roles manifest: %s", err.Error())
	}

	for _, role := range rolesManifest.Roles {
		imageName := builder.GetRoleDevImageName(repository, role, role.GetRoleDevVersion())

		if !existingOnDocker {
			f.UI.Println(imageName)
			continue
		}

		image, err := dockerManager.FindImage(imageName)

		if err == docker.ErrImageNotFound {
			continue
		} else if err != nil {
			return fmt.Errorf("Error looking up image: %s", err.Error())
		}

		if withVirtualSize {
			f.UI.Printf(
				"%s (%sMB)\n",
				color.GreenString(imageName),
				color.YellowString("%.2f", float64(image.VirtualSize)/(1024*1024)),
			)
		} else {
			f.UI.Println(imageName)
		}
	}

	return nil
}

//LoadReleases loads information about BOSH releases
func (f *Fissile) LoadReleases(releasePaths, releaseNames, releaseVersions []string, cacheDir string) error {
	releases := make([]*model.Release, len(releasePaths))

	for idx, releasePath := range releasePaths {
		var releaseName, releaseVersion string

		if len(releaseNames) != 0 {
			releaseName = releaseNames[idx]
		}

		if len(releaseVersions) != 0 {
			releaseVersion = releaseVersions[idx]
		}

		release, err := model.NewDevRelease(releasePath, releaseName, releaseVersion, cacheDir)
		if err != nil {
			return fmt.Errorf("Error loading release information: %s", err.Error())
		}

		releases[idx] = release
	}

	f.releases = releases
	err := f.injectPatchPropertiesJobSpec()
	if err != nil {
		return fmt.Errorf("Error loading release information: %s", err)
	}
	return nil
}

// DiffConfigurationBases generates a diff comparing the specs for two different BOSH releases
func (f *Fissile) DiffConfigurationBases(releasePaths []string, cacheDir string) error {
	hashDiffs, err := f.GetDiffConfigurationBases(releasePaths, cacheDir)
	if err != nil {
		return err
	}
	f.reportHashDiffs(hashDiffs)
	return nil
}

// GetDiffConfigurationBases calculates the difference in configs and returns a hash
func (f *Fissile) GetDiffConfigurationBases(releasePaths []string, cacheDir string) (*HashDiffs, error) {
	if len(releasePaths) != 2 {
		return nil, fmt.Errorf("expected two release paths, got %d", len(releasePaths))
	}
	defaultValues := []string{}
	err := f.LoadReleases(releasePaths, defaultValues, defaultValues, cacheDir)
	if err != nil {
		return nil, fmt.Errorf("dev config diff: error loading release information: %s", err)
	}
	return getDiffsFromReleases(f.releases)
}

// Since each job processes only its own spec, inject the designated
// patches-properties pseudo-job's spec into all the other jobs' specs.
func (f *Fissile) injectPatchPropertiesJobSpec() error {
	if f.patchPropertiesReleaseName == "" || f.patchPropertiesJobName == "" {
		return nil
	}
	var patchPropertiesJob *model.Job
	for _, release := range f.releases {
		if release.Name == f.patchPropertiesReleaseName {
			job, _ := release.LookupJob(f.patchPropertiesJobName)
			if job != nil {
				patchPropertiesJob = job
			}
		}
	}
	if patchPropertiesJob == nil {
		return nil
	}
	for _, release := range f.releases {
		for _, job := range release.Jobs {
			if release.Name != f.patchPropertiesReleaseName || job != patchPropertiesJob {
				job.MergeSpec(patchPropertiesJob)
			}
		}
	}
	return nil
}

type keyHash map[string]string

// HashDiffs summarizes the diffs between the two configs
type HashDiffs struct {
	AddedKeys     []string
	DeletedKeys   []string
	ChangedValues map[string][2]string
}

func (f *Fissile) reportHashDiffs(hashDiffs *HashDiffs) {
	if len(hashDiffs.DeletedKeys) > 0 {
		f.UI.Println(color.RedString("Dropped keys:"))
		sort.Strings(hashDiffs.DeletedKeys)
		for _, v := range hashDiffs.DeletedKeys {
			f.UI.Printf("  %s\n", v)
		}
	}
	if len(hashDiffs.AddedKeys) > 0 {
		f.UI.Println(color.GreenString("Added keys:"))
		sort.Strings(hashDiffs.AddedKeys)
		for _, v := range hashDiffs.AddedKeys {
			f.UI.Printf("  %s\n", v)
		}
	}
	if len(hashDiffs.ChangedValues) > 0 {
		f.UI.Println(color.BlueString("Changed values:"))
		sortedKeys := make([]string, len(hashDiffs.ChangedValues))
		i := 0
		for k := range hashDiffs.ChangedValues {
			sortedKeys[i] = k
			i++
		}
		sort.Strings(sortedKeys)
		for _, k := range sortedKeys {
			v := hashDiffs.ChangedValues[k]
			f.UI.Printf("  %s: %s => %s\n", k, v[0], v[1])
		}
	}
}

func getDiffsFromReleases(releases []*model.Release) (*HashDiffs, error) {
	hashes := [2]keyHash{keyHash{}, keyHash{}}
	for idx, release := range releases {
		configs := release.GetUniqueConfigs()
		for _, config := range configs {
			hashes[idx][config.Name] = config.Description
		}
		// Get the spec configs
		for _, job := range release.Jobs {
			for _, property := range job.Properties {
				key := fmt.Sprintf("%s.%s.%s", release.Name, job.Name, property.Name)
				hashes[idx][key] = fmt.Sprintf("%+v", property.Default)
			}
		}
	}
	return compareHashes(hashes[0], hashes[1]), nil
}

func compareHashes(v1Hash, v2Hash keyHash) *HashDiffs {
	changed := map[string][2]string{}
	deleted := []string{}
	added := []string{}

	for k, v := range v1Hash {
		v2, ok := v2Hash[k]
		if !ok {
			deleted = append(deleted, k)
		} else if v != v2 {
			changed[k] = [2]string{v, v2}
		}
	}
	for k := range v2Hash {
		_, ok := v1Hash[k]
		if !ok {
			added = append(added, k)
		}
	}
	return &HashDiffs{AddedKeys: added, DeletedKeys: deleted, ChangedValues: changed}
}

// GenerateKube will create a set of configuration files suitable for deployment
// on Kubernetes
func (f *Fissile) GenerateKube(rolesManifestPath, outputDir, repository, registry, organization string, defaultFiles []string, useMemoryLimits bool, skipDev bool) error {

	rolesManifest, err := model.LoadRoleManifest(rolesManifestPath, f.releases, skipDev)
	if err != nil {
		return fmt.Errorf("Error loading roles manifest: %s", err.Error())
	}

	f.UI.Println("Loading defaults from env files")
	defaults, err := godotenv.Read(defaultFiles...)
	if err != nil {
		return err
	}

	settings := &kube.ExportSettings{
		Defaults:        defaults,
		Registry:        registry,
		Organization:    organization,
		Repository:      repository,
		UseMemoryLimits: useMemoryLimits,
	}

	for _, role := range rolesManifest.Roles {
		roleTypeDir := filepath.Join(outputDir, string(role.Type))
		if err = os.MkdirAll(roleTypeDir, 0755); err != nil {
			return err
		}
		outputPath := filepath.Join(roleTypeDir, fmt.Sprintf("%s.yml", role.Name))

		f.UI.Printf("Writing config %s for role %s\n",
			color.CyanString(outputPath),
			color.CyanString(role.Name),
		)

		outputFile, err := os.Create(outputPath)
		if err != nil {
			return err
		}
		defer outputFile.Close()

		switch role.Type {
		case model.RoleTypeBoshTask:
			job, err := kube.NewJob(role, settings)
			if err != nil {
				return err
			}

			if err := kube.WriteYamlConfig(job, outputFile); err != nil {
				return err
			}

		case model.RoleTypeBosh:
			needsStorage := len(role.Run.PersistentVolumes) != 0 || len(role.Run.SharedVolumes) != 0

			if role.HasTag("clustered") || needsStorage {
				statefulSet, deps, err := kube.NewStatefulSet(role, settings)
				if err != nil {
					return err
				}

				if err := kube.WriteYamlConfig(statefulSet, outputFile); err != nil {
					return err
				}

				if err := kube.WriteYamlConfig(deps, outputFile); err != nil {
					return err
				}

				continue
			}

			deployment, svc, err := kube.NewDeployment(role, settings)
			if err != nil {
				return err
			}

			if err := kube.WriteYamlConfig(deployment, outputFile); err != nil {
				return err
			}

			if svc != nil {
				if err := kube.WriteYamlConfig(svc, outputFile); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

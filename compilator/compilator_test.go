package compilator

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hpcloud/fissile/docker"
	"github.com/hpcloud/fissile/model"
	"github.com/hpcloud/fissile/scripts/compilation"
	"github.com/hpcloud/fissile/util"

	dockerclient "github.com/fsouza/go-dockerclient"
	"github.com/hpcloud/termui"
	"github.com/pborman/uuid"
	"github.com/stretchr/testify/assert"
)

const (
	dockerImageEnvVar      = "FISSILE_TEST_DOCKER_IMAGE"
	defaultDockerTestImage = "ubuntu:14.04"
)

var dockerImageName string

var ui = termui.New(
	&bytes.Buffer{},
	ioutil.Discard,
	nil,
)

func TestMain(m *testing.M) {
	dockerImageName = os.Getenv(dockerImageEnvVar)
	if dockerImageName == "" {
		dockerImageName = defaultDockerTestImage
	}

	retCode := m.Run()

	os.Exit(retCode)
}

func TestCompilationEmpty(t *testing.T) {
	assert := assert.New(t)

	c, err := NewCompilator(nil, "", "", "", "", "", false, ui)
	assert.NoError(err)

	waitCh := make(chan struct{})
	go func() {
		err := c.Compile(1, genTestCase(), nil)
		close(waitCh)
		assert.NoError(err)
	}()

	<-waitCh
}

func TestCompilationBasic(t *testing.T) {
	assert := assert.New(t)

	file, err := ioutil.TempFile("", "metrics")
	assert.NoError(err)

	metrics := file.Name()
	defer os.Remove(metrics)

	saveCompilePackage := compilePackageHarness
	defer func() {
		compilePackageHarness = saveCompilePackage
	}()

	compileChan := make(chan string)
	compilePackageHarness = func(c *Compilator, pkg *model.Package) error {
		compileChan <- pkg.Name
		return nil
	}

	c, err := NewCompilator(nil, "", metrics, "", "", "", false, ui)
	assert.NoError(err)

	release := genTestCase("ruby-2.5", "consul>go-1.4", "go-1.4")

	waitCh := make(chan struct{})
	go func() {
		c.Compile(1, release, nil)
		close(waitCh)
	}()

	for _, expectedName := range []string{"ruby-2.5", "go-1.4", "consul"} {
		select {
		case pkgName := <-compileChan:
			assert.Equal(pkgName, expectedName)
		case <-time.After(1 * time.Second):
			assert.Fail("Timed out waiting for compile result", expectedName)
		}
	}
	select {
	case <-waitCh:
	case <-time.After(1 * time.Second):
		assert.Fail("Timed out waiting for overall completion")
	}

	expected := []string{
		",compile-packages::test-release/ruby-2.5,start",
		",compile-packages::wait::test-release/ruby-2.5,start",
		",compile-packages::wait::test-release/ruby-2.5,done",
		",compile-packages::run::test-release/ruby-2.5,start",
		",compile-packages::run::test-release/ruby-2.5,done",
		",compile-packages::test-release/ruby-2.5,done",
		",compile-packages::test-release/go-1.4,start",
		",compile-packages::wait::test-release/go-1.4,start",
		",compile-packages::wait::test-release/go-1.4,done",
		",compile-packages::run::test-release/go-1.4,start",
		",compile-packages::run::test-release/go-1.4,done",
		",compile-packages::test-release/go-1.4,done",
		",compile-packages::test-release/consul,start",
		",compile-packages::wait::test-release/consul,start",
		",compile-packages::wait::test-release/consul,done",
		",compile-packages::run::test-release/consul,start",
		",compile-packages::run::test-release/consul,done",
		",compile-packages::test-release/consul,done",
	}

	contents, err := ioutil.ReadFile(metrics)
	assert.NoError(err)

	actual := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if assert.Len(actual, len(expected)) {
		for lineno, suffix := range expected {
			if !strings.HasSuffix(actual[lineno], suffix) {
				assert.Fail(fmt.Sprintf("Doesn't have suffix: \n"+
					"value: %s\nsuffix: %s\n",
					actual[lineno], suffix))
			}
		}
	}
}

func TestCompilationSkipCompiled(t *testing.T) {
	saveCompilePackage := compilePackageHarness
	saveIsPackageCompiled := isPackageCompiledHarness
	defer func() {
		compilePackageHarness = saveCompilePackage
		isPackageCompiledHarness = saveIsPackageCompiled
	}()

	compileChan := make(chan string)
	compilePackageHarness = func(c *Compilator, pkg *model.Package) error {
		compileChan <- pkg.Name
		return nil
	}

	isPackageCompiledHarness = func(c *Compilator, pkg *model.Package) (bool, error) {
		return pkg.Name == "ruby-2.5", nil
	}

	assert := assert.New(t)

	c, err := NewCompilator(nil, "", "", "", "", "", false, ui)
	assert.NoError(err)

	release := genTestCase("ruby-2.5", "consul>go-1.4", "go-1.4")

	waitCh := make(chan struct{})
	go func() {
		c.Compile(1, release, nil)
		close(waitCh)
	}()

	assert.Equal(<-compileChan, "go-1.4")
	assert.Equal(<-compileChan, "consul")
	<-waitCh
}

func TestCompilationRoleManifest(t *testing.T) {
	saveCompilePackage := compilePackageHarness
	defer func() {
		compilePackageHarness = saveCompilePackage
	}()

	compileChan := make(chan string, 2)
	compilePackageHarness = func(c *Compilator, pkg *model.Package) error {
		compileChan <- pkg.Name
		return nil
	}

	assert := assert.New(t)

	c, err := NewCompilator(nil, "", "", "", "", "", false, ui)
	assert.NoError(err)

	workDir, err := os.Getwd()
	assert.NoError(err)

	releasePath := filepath.Join(workDir, "../test-assets/tor-boshrelease")
	releasePathBoshCache := filepath.Join(releasePath, "bosh-cache")
	release, err := model.NewDevRelease(releasePath, "", "", releasePathBoshCache)
	assert.NoError(err)
	// This release has 3 packages:
	// `tor` is in the role manifest, and will be included
	// `libevent` is a dependency of `tor`, and will be included even though it's not in the role manifest
	// `boguspackage` is neither, and will not be included

	roleManifestPath := filepath.Join(workDir, "../test-assets/role-manifests/tor-good.yml")
	roleManifest, err := model.LoadRoleManifest(roleManifestPath, []*model.Release{release}, false)
	assert.NoError(err)
	assert.NotNil(roleManifest)

	waitCh := make(chan struct{})
	errCh := make(chan error)
	go func() {
		errCh <- c.Compile(1, []*model.Release{release}, roleManifest)
	}()
	go func() {
		// `libevent` is a dependency of `tor` and will be compiled first
		assert.NoError(<-errCh)
		assert.Equal(<-compileChan, "libevent")
		assert.Equal(<-compileChan, "tor")
		close(waitCh)
	}()

	select {
	case <-waitCh:
		return
	case <-time.After(5 * time.Second):
		assert.Fail("Test timeout")
	}
}

// getContainerIDs returns all (running or not) containers with the given image
func getContainerIDs(imageName string) ([]string, error) {
	var results []string

	client, err := dockerclient.NewClientFromEnv()
	if err != nil {
		return nil, err
	}

	containers, err := client.ListContainers(dockerclient.ListContainersOptions{
		All: true,
	})
	if err != nil {
		return nil, err
	}
	for _, container := range containers {
		if container.Image == imageName {
			results = append(results, container.ID)
		}
	}
	return results, nil
}

func TestContainerKeptAfterCompilationWithErrors(t *testing.T) {
	doTestContainerKeptAfterCompilationWithErrors(t, true)
	doTestContainerKeptAfterCompilationWithErrors(t, false)
}

func doTestContainerKeptAfterCompilationWithErrors(t *testing.T, keepContainer bool) {
	assert := assert.New(t)

	compilationWorkDir, err := util.TempDir("", "fissile-tests")
	assert.NoError(err)
	defer os.RemoveAll(compilationWorkDir)

	dockerManager, err := docker.NewImageManager()
	assert.NoError(err)

	workDir, err := os.Getwd()

	releasePath := filepath.Join(workDir, "../test-assets/corrupt-releases/corrupt-package")
	releasePathBoshCache := filepath.Join(releasePath, "bosh-cache")
	release, err := model.NewDevRelease(releasePath, "", "", releasePathBoshCache)
	assert.NoError(err)

	testRepository := fmt.Sprintf("fissile-test-compilator-%s", uuid.New())

	comp, err := NewCompilator(dockerManager, compilationWorkDir, "", testRepository, compilation.FakeBase, "3.14.15", keepContainer, ui)
	assert.NoError(err)

	imageName := comp.BaseImageName()

	_, err = comp.CreateCompilationBase(dockerImageName)
	defer func() {
		err = dockerManager.RemoveImage(imageName)
		assert.NoError(err)
	}()
	assert.NoError(err)
	beforeCompileContainers, err := getContainerIDs(imageName)
	assert.NoError(err)

	comp.baseType = compilation.FailBase
	err = comp.compilePackage(release.Packages[0])
	// We expect the package to fail this time.
	assert.Error(err)
	afterCompileContainers, err := getContainerIDs(imageName)
	assert.NoError(err)

	// If keepInContainer is on,
	// We expect one more container, so we'll need to explicitly
	// remove it so the deferred func can call dockerManager.RemoveImage

	droppedIDs := findStringSetDifference(beforeCompileContainers, afterCompileContainers)
	assert.Empty(droppedIDs, fmt.Sprintf("%d IDs were dropped during the failed compile", len(droppedIDs)))

	addedIDs := findStringSetDifference(afterCompileContainers, beforeCompileContainers)
	if keepContainer {
		assert.Len(addedIDs, 1)
	} else {
		assert.Empty(addedIDs)
	}

	client, err := dockerclient.NewClientFromEnv()
	assert.NoError(err)

	if keepContainer {
		for _, containerID := range addedIDs {
			container, err := client.InspectContainer(containerID)
			if !assert.NoError(err) {
				continue
			}
			err = client.StopContainer(container.ID, 5)
			assert.NoError(err)
			err = dockerManager.RemoveContainer(container.ID)
			assert.NoError(err)
			err = dockerManager.RemoveVolumes(container)
			assert.NoError(err)
		}
	}

	// Clean up any unexpected volumes (there should not be any)
	volumes, err := client.ListVolumes(dockerclient.ListVolumesOptions{
		Filters: map[string][]string{"name": []string{testRepository}},
	})
	if assert.NoError(err) && !assert.Empty(volumes) {
		for _, volume := range volumes {
			err = client.RemoveVolume(volume.Name)
			assert.NoError(err)
		}
	}
}

// findStringSetDifference returns all strings in the |from| set not in |subset|
func findStringSetDifference(from, subset []string) []string {
	var results []string
	for _, left := range from {
		found := false
		for _, right := range subset {
			if left == right {
				found = true
				break
			}
		}
		if !found {
			results = append(results, left)
		}
	}
	return results
}

// TestCompilationMultipleErrors checks that we correctly deal with multiple compilations failing
func TestCompilationMultipleErrors(t *testing.T) {
	saveCompilePackage := compilePackageHarness
	saveIsPackageCompiled := isPackageCompiledHarness
	defer func() {
		compilePackageHarness = saveCompilePackage
		isPackageCompiledHarness = saveIsPackageCompiled
	}()

	compilePackageHarness = func(c *Compilator, pkg *model.Package) error {
		return fmt.Errorf("Intentional error compiling %s", pkg.Name)
	}

	isPackageCompiledHarness = func(c *Compilator, pkg *model.Package) (bool, error) {
		return false, nil
	}

	assert := assert.New(t)

	c, err := NewCompilator(nil, "", "", "", "", "", false, ui)
	assert.NoError(err)

	release := genTestCase("ruby-2.5", "consul>go-1.4", "go-1.4")

	err = c.Compile(1, release, nil)
	assert.NotNil(err)
}

func TestGetPackageStatusCompiled(t *testing.T) {
	assert := assert.New(t)

	compilationWorkDir, err := util.TempDir("", "fissile-tests")
	assert.NoError(err)
	defer os.RemoveAll(compilationWorkDir)

	dockerManager, err := docker.NewImageManager()
	assert.NoError(err)

	workDir, err := os.Getwd()
	ntpReleasePath := filepath.Join(workDir, "../test-assets/ntp-release")
	ntpReleasePathBoshCache := filepath.Join(ntpReleasePath, "bosh-cache")
	release, err := model.NewDevRelease(ntpReleasePath, "", "", ntpReleasePathBoshCache)
	// For this test we assume that the release does not have multiple packages with a single fingerprint
	assert.NoError(err)

	compilator, err := NewCompilator(dockerManager, compilationWorkDir, "", "fissile-test-compilator", compilation.FakeBase, "3.14.15", false, ui)
	assert.NoError(err)

	compiledPackagePath := filepath.Join(compilationWorkDir, release.Packages[0].Fingerprint, "compiled")

	err = os.MkdirAll(compiledPackagePath, 0755)
	assert.NoError(err)

	err = ioutil.WriteFile(filepath.Join(compiledPackagePath, "foo"), []byte{}, 0700)
	assert.NoError(err)

	status, err := compilator.isPackageCompiled(release.Packages[0])

	assert.NoError(err)
	assert.True(status)
}

// TestCompilationParallel checks that we compile multiple releases in parallel
func TestCompilationParallel(t *testing.T) {
	// We make two releases, with one package each, and block until both
	// packages have _started_ compiling.  This proves that we're doing compiles
	// of packages across releases in parallel.  Note that neither package
	// depends on the other, as far as the rest of the system is concerned; if
	// they did, we wouldn't get the desired parallel compilation behaviour.

	releases := []*model.Release{
		&model.Release{Name: "release-one"},
		&model.Release{Name: "release-two"},
	}
	releases[0].Packages = []*model.Package{
		&model.Package{
			Name:        "package-one",
			Fingerprint: "package-one",
			Release:     releases[0],
		},
	}
	releases[1].Packages = []*model.Package{
		&model.Package{
			Name:        "package-two",
			Fingerprint: "package-two",
			Release:     releases[1],
		},
	}

	saveCompilePackage := compilePackageHarness
	saveIsPackageCompiled := isPackageCompiledHarness
	defer func() {
		compilePackageHarness = saveCompilePackage
		isPackageCompiledHarness = saveIsPackageCompiled
	}()

	mutex := sync.Mutex{}
	cond := sync.NewCond(&mutex)
	compiledPackages := make(map[string]bool)
	compilePackageHarness = func(c *Compilator, pkg *model.Package) error {
		mutex.Lock()
		defer mutex.Unlock()
		compiledPackages[pkg.Name] = true
		other := map[string]string{
			"package-one": "package-two",
			"package-two": "package-one",
		}[pkg.Name]
		if compiledPackages[other] {
			// The other package has started compiling and is waiting for us
			cond.Signal()
		} else {
			// The other package hasn't started yet, wait for it to start
			cond.Wait()
		}
		// At this point, _both_ packages have started
		return nil
	}

	isPackageCompiledHarness = func(c *Compilator, pkg *model.Package) (bool, error) {
		return false, nil
	}

	assert := assert.New(t)

	c, err := NewCompilator(nil, "", "", "", "", "", false, ui)
	assert.NoError(err)

	testDoneCh := make(chan struct{})
	go func() {
		err = c.Compile(2, releases, nil)
		assert.NoError(err)
		close(testDoneCh)
	}()
	select {
	case <-testDoneCh:
	// nothing
	case <-time.After(5 * time.Second):
		assert.Fail("Timed out running test")
		// Try to unwedge things.  Not that it matters, we're bailing out of
		// the test - but it's nice to let the goroutine die.
		cond.Broadcast()
	}
}

func TestGetPackageStatusNone(t *testing.T) {
	assert := assert.New(t)

	compilationWorkDir, err := util.TempDir("", "fissile-tests")
	assert.NoError(err)
	defer os.RemoveAll(compilationWorkDir)

	dockerManager, err := docker.NewImageManager()
	assert.NoError(err)

	workDir, err := os.Getwd()
	ntpReleasePath := filepath.Join(workDir, "../test-assets/ntp-release")
	ntpReleasePathBoshCache := filepath.Join(ntpReleasePath, "bosh-cache")
	release, err := model.NewDevRelease(ntpReleasePath, "", "", ntpReleasePathBoshCache)
	// For this test we assume that the release does not have multiple packages with a single fingerprint
	assert.NoError(err)

	compilator, err := NewCompilator(dockerManager, compilationWorkDir, "", "fissile-test-compilator", compilation.FakeBase, "3.14.15", false, ui)
	assert.NoError(err)

	status, err := compilator.isPackageCompiled(release.Packages[0])

	assert.NoError(err)
	assert.False(status)
}

func TestPackageFolderStructure(t *testing.T) {
	assert := assert.New(t)

	compilationWorkDir, err := util.TempDir("", "fissile-tests")
	assert.NoError(err)
	defer os.RemoveAll(compilationWorkDir)

	dockerManager, err := docker.NewImageManager()
	assert.NoError(err)

	workDir, err := os.Getwd()
	ntpReleasePath := filepath.Join(workDir, "../test-assets/ntp-release")
	ntpReleasePathBoshCache := filepath.Join(ntpReleasePath, "bosh-cache")
	release, err := model.NewDevRelease(ntpReleasePath, "", "", ntpReleasePathBoshCache)
	assert.NoError(err)

	compilator, err := NewCompilator(dockerManager, compilationWorkDir, "", "fissile-test-compilator", compilation.FakeBase, "3.14.15", false, ui)
	assert.NoError(err)

	err = compilator.createCompilationDirStructure(release.Packages[0])
	assert.NoError(err)

	exists, err := validatePath(compilator.getDependenciesPackageDir(release.Packages[0]), true, "")
	assert.NoError(err)
	assert.True(exists)

	exists, err = validatePath(compilator.getSourcePackageDir(release.Packages[0]), true, "")
	assert.NoError(err)
	assert.True(exists)
}

func TestPackageDependenciesPreparation(t *testing.T) {
	assert := assert.New(t)

	compilationWorkDir, err := util.TempDir("", "fissile-tests")
	assert.NoError(err)
	defer os.RemoveAll(compilationWorkDir)

	dockerManager, err := docker.NewImageManager()
	assert.NoError(err)

	workDir, err := os.Getwd()
	torReleasePath := filepath.Join(workDir, "../test-assets/tor-boshrelease")
	torReleasePathBoshCache := filepath.Join(torReleasePath, "bosh-cache")
	release, err := model.NewDevRelease(torReleasePath, "", "", torReleasePathBoshCache)
	assert.NoError(err)

	compilator, err := NewCompilator(dockerManager, compilationWorkDir, "", "fissile-test-compilator", compilation.FakeBase, "3.14.15", false, ui)
	assert.NoError(err)

	pkg, err := release.LookupPackage("tor")
	assert.NoError(err)
	err = compilator.createCompilationDirStructure(pkg)
	assert.NoError(err)
	err = os.MkdirAll(pkg.Dependencies[0].GetPackageCompiledDir(compilator.hostWorkDir), 0755)
	assert.NoError(err)

	dummyCompiledFile := filepath.Join(pkg.Dependencies[0].GetPackageCompiledDir(compilator.hostWorkDir), "foo")
	file, err := os.Create(dummyCompiledFile)
	assert.NoError(err)
	file.Close()

	err = compilator.copyDependencies(pkg)
	assert.NoError(err)

	expectedDummyFileLocation := filepath.Join(compilator.getDependenciesPackageDir(pkg), pkg.Dependencies[0].Name, "foo")
	exists, err := validatePath(expectedDummyFileLocation, false, "")
	assert.NoError(err)
	assert.True(exists, expectedDummyFileLocation)
}

func TestCompilePackage(t *testing.T) {
	doTestCompilePackage(t, true)
	doTestCompilePackage(t, false)
}

func doTestCompilePackage(t *testing.T, keepInContainer bool) {
	assert := assert.New(t)

	compilationWorkDir, err := util.TempDir("", "fissile-tests")
	assert.NoError(err)
	defer os.RemoveAll(compilationWorkDir)

	dockerManager, err := docker.NewImageManager()
	assert.NoError(err)

	workDir, err := os.Getwd()
	ntpReleasePath := filepath.Join(workDir, "../test-assets/ntp-release")
	ntpReleasePathBoshCache := filepath.Join(ntpReleasePath, "bosh-cache")
	release, err := model.NewDevRelease(ntpReleasePath, "", "", ntpReleasePathBoshCache)
	assert.NoError(err)

	testRepository := fmt.Sprintf("fissile-test-compilator-%s", uuid.New())

	comp, err := NewCompilator(dockerManager, compilationWorkDir, "", testRepository, compilation.FakeBase, "3.14.15", keepInContainer, ui)
	assert.NoError(err)

	imageName := comp.BaseImageName()

	_, err = comp.CreateCompilationBase(dockerImageName)
	defer func() {
		err = dockerManager.RemoveImage(imageName)
		assert.NoError(err)
	}()
	assert.NoError(err)
	beforeCompileContainers, err := getContainerIDs(imageName)
	assert.NoError(err)

	err = comp.compilePackage(release.Packages[0])
	assert.NoError(err)
	afterCompileContainers, err := getContainerIDs(imageName)
	assert.NoError(err)
	assert.Equal(beforeCompileContainers, afterCompileContainers)
}

func TestCreateDepBuckets(t *testing.T) {
	t.Parallel()

	packages := []*model.Package{
		{
			Name:        "consul",
			Fingerprint: "CO",
			Dependencies: []*model.Package{
				{Fingerprint: "GO", Name: "go-1.4"},
			},
		},
		{
			Name:         "go-1.4",
			Fingerprint:  "GO",
			Dependencies: nil,
		},
		{
			Name:        "cloud_controller_go",
			Fingerprint: "CC",
			Dependencies: []*model.Package{
				{Fingerprint: "GO", Name: "go-1.4"},
				{Fingerprint: "RU", Name: "ruby-2.5"},
			},
		},
		{
			Name:         "ruby-2.5",
			Fingerprint:  "RU",
			Dependencies: nil,
		},
	}

	buckets := createDepBuckets(packages)
	assert.Equal(t, len(buckets), 4)
	assert.Equal(t, buckets[0].Name, "ruby-2.5") // Ruby should be first
	assert.Equal(t, buckets[1].Name, "go-1.4")
	assert.Equal(t, buckets[2].Name, "consul")
	assert.Equal(t, buckets[3].Name, "cloud_controller_go")
}

func TestCreateDepBucketsOnChain(t *testing.T) {
	t.Parallel()

	packages := []*model.Package{
		{
			Fingerprint:  "a",
			Name:         "A",
			Dependencies: nil,
		},
		{
			Fingerprint: "b",
			Name:        "B",
			Dependencies: []*model.Package{{
				Fingerprint: "c",
				Name:        "C",
			}},
		},
		{
			Fingerprint: "c",
			Name:        "C",
			Dependencies: []*model.Package{{
				Fingerprint: "a",
				Name:        "A",
			}},
		},
	}

	buckets := createDepBuckets(packages)
	assert.Equal(t, len(buckets), 3)
	assert.Equal(t, buckets[0].Name, "A")
	assert.Equal(t, buckets[1].Name, "C")
	assert.Equal(t, buckets[2].Name, "B")
}

func TestGatherPackages(t *testing.T) {
	assert := assert.New(t)

	c, err := NewCompilator(nil, "", "", "", "", "", false, ui)
	assert.NoError(err)

	releases := genTestCase("ruby-2.5", "go-1.4.1:G", "go-1.4:G")
	packages := c.gatherPackages(releases, nil)

	assert.Len(packages, 2)
	assert.Equal(packages[0].Name, "ruby-2.5")
	assert.Equal(packages[1].Name, "go-1.4.1")
}

func TestRemoveCompiledPackages(t *testing.T) {
	saveIsPackageCompiled := isPackageCompiledHarness
	defer func() {
		isPackageCompiledHarness = saveIsPackageCompiled
	}()

	isPackageCompiledHarness = func(c *Compilator, pkg *model.Package) (bool, error) {
		return pkg.Name == "ruby-2.5", nil
	}

	assert := assert.New(t)

	c, err := NewCompilator(nil, "", "", "", "", "", false, ui)
	assert.NoError(err)

	releases := genTestCase("ruby-2.5", "consul>go-1.4", "go-1.4")

	packages, err := c.removeCompiledPackages(c.gatherPackages(releases, nil))
	assert.NoError(err)

	assert.Len(packages, 2)
	assert.Equal(packages[0].Name, "consul")
	assert.Equal(packages[1].Name, "go-1.4")
}

func genTestCase(args ...string) []*model.Release {
	var packages []*model.Package
	release := model.Release{
		Name: "test-release",
	}

	for _, pkgDef := range args {
		// Split definition into name+fingerprint and dependencies
		splits := strings.Split(pkgDef, ">")
		pkgName := splits[0]

		var deps []*model.Package
		if len(splits) == 2 {
			pkgDeps := strings.Split(splits[1], ",")

			for _, dep := range pkgDeps {
				deps = append(deps, &model.Package{Release: &release, Name: dep, Fingerprint: dep})
			}
		}

		// Split n+f into name and fingerprint
		splits = strings.Split(pkgName, ":")
		pkgName = splits[0]

		var pkgFingerprint string
		if len(splits) == 2 {
			pkgFingerprint = splits[1]
		} else {
			pkgFingerprint = pkgName
		}

		packages = append(packages, &model.Package{
			Release:      &release,
			Name:         pkgName,
			Fingerprint:  pkgFingerprint,
			Dependencies: deps,
		})
	}
	release.Packages = packages

	return []*model.Release{&release}
}

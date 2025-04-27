package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	//"switch-gui-temp/db" // Assuming this path is correct for your local db types
	"sync"

	// Removed astilectron imports
	// "github.com/asticode/go-astikit"
	// "github.com/asticode/go-astilectron"
	// bootstrap "github.com/asticode/go-astilectron-bootstrap"

	"github.com/trembon/switch-library-manager/db"
	"github.com/trembon/switch-library-manager/process"
	"github.com/trembon/switch-library-manager/settings"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime" // Added for Wails runtime functions
	"go.uber.org/zap"
)

//go:embed all:frontend/dist
var assets embed.FS

type Pair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type LocalLibraryData struct {
	LibraryData []LibraryTemplateData `json:"library_data"`
	Issues      []Pair                `json:"issues"`
	NumFiles    int                   `json:"num_files"`
}

type SwitchTitle struct {
	Name        string `json:"name"`
	TitleId     string `json:"titleId"`
	Icon        string `json:"icon"`
	Region      string `json:"region"`
	ReleaseDate string `json:"release_date"`
}

type LibraryTemplateData struct {
	Id      int    `json:"id"` // Consider if this ID is still needed or how it's generated
	Name    string `json:"name"`
	Version string `json:"version"`
	Dlc     string `json:"dlc"` // Consider if this field is populated/used
	TitleId string `json:"titleId"`
	Path    string `json:"path"`
	Icon    string `json:"icon"`
	Update  int    `json:"update"`
	Region  string `json:"region"`
	Type    string `json:"type"`
}

type ProgressUpdate struct {
	Curr    int    `json:"curr"`
	Total   int    `json:"total"`
	Message string `json:"message"`
}

// Removed State struct as window is no longer needed.
// Mutex might be needed depending on how methods are called and interact with shared DBs.
// Added Mutex directly to GUI struct for now.
// type State struct {
// 	sync.Mutex
// 	switchDB *db.SwitchTitlesDB
// 	localDB  *db.LocalSwitchFilesDB
// 	//window   *astilectron.Window // Removed
// }

// Removed Message struct
// type Message struct {
// 	Name    string `json:"name"`
// 	Payload string `json:"payload"`
// }

type GUI struct {
	sync.Mutex     // Added mutex directly
	ctx            context.Context
	baseFolder     string
	localDbManager *db.LocalSwitchDBManager
	sugarLogger    *zap.SugaredLogger
	switchDB       *db.SwitchTitlesDB     // Added field
	localDB        *db.LocalSwitchFilesDB // Added field
}

func CreateGUI(baseFolder string, sugarLogger *zap.SugaredLogger) *GUI {
	// Initialize fields, ctx will be set during startup
	return &GUI{baseFolder: baseFolder, sugarLogger: sugarLogger}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (g *GUI) startup(ctx context.Context) {
	g.ctx = ctx
	// Perform initial actions that might require context or happen after frontend is ready
	// e.g., load initial settings, check for updates async?
	go func() { // Run check for updates in background to not block startup
		newUpdate, err := settings.CheckForUpdates()
		if err != nil {
			g.sugarLogger.Errorf("Failed to check for updates on startup: %v", err)
			// Optionally emit an event to the frontend about the error
			// runtime.EventsEmit(g.ctx, "error", fmt.Sprintf("Update check failed: %v", err))
		} else if newUpdate {
			// Emit an event to notify the frontend about the update
			runtime.EventsEmit(g.ctx, "newUpdateAvailable", true)
		}
	}()
}

func (g *GUI) Start() {
	var err error
	g.localDbManager, err = db.NewLocalSwitchDBManager(g.baseFolder)
	if err != nil {
		// Use logger for startup errors before Wails is running
		g.sugarLogger.Errorf("Failed to create local files db: %v", err)
		fmt.Printf("Failed to create local files db: %v\n", err) // Also print to console
		return
	}
	defer g.localDbManager.Close()

	// Initialize keys if needed before GUI starts or within a bound method
	_, keyErr := settings.InitSwitchKeys(g.baseFolder)
	if keyErr != nil {
		g.sugarLogger.Warnf("Failed to initialize switch keys: %v", keyErr)
		// Decide if this is a fatal error or just a warning
	}

	// --- Wails App Setup ---
	err = wails.Run(&options.App{
		Title:  "Switch Library Manager (" + settings.SLM_VERSION + ")",
		Width:  1200,
		Height: 700, // Adjusted height slightly
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 51, G: 51, B: 51, A: 1}, // Corresponds to #333
		OnStartup:        g.startup,                                // Use the struct method
		Bind: []interface{}{ // Bind the GUI struct itself to expose its methods
			g,
		},
		// Add Menu/Window options as needed using Wails options
		// Menu: menu.NewMenuFromItems( ... ),
	})

	if err != nil {
		g.sugarLogger.Fatalf("Error running Wails app: %v", err) // Use Fatalf for critical errors
	}
}

// Removed handleMessage function as Wails uses method binding

// --- Bound Methods (Public methods callable from Javascript) ---

// OrganizeLibrary triggers the library organization process.
func (g *GUI) OrganizeLibrary() error {
	g.Lock()
	defer g.Unlock()

	if g.localDB == nil || g.switchDB == nil {
		return errors.New("databases not loaded. Please scan library first")
	}

	folderToScan := settings.ReadSettings(g.baseFolder).Folder
	options := settings.ReadSettings(g.baseFolder).OrganizeOptions
	if !process.IsOptionsValid(options) {
		errMsg := "the organize options in settings.json are not valid, please check that the template contains file/folder name"
		g.sugarLogger.Error(errMsg)
		// runtime.EventsEmit(g.ctx, "error", errMsg) // Emit error event
		return errors.New(errMsg) // Return error to frontend caller
	}

	// Run organization in a goroutine to avoid blocking the UI thread
	// and report progress back via events.
	go func() {
		g.sugarLogger.Info("Starting library organization...")
		process.OrganizeByFolders(folderToScan, g.localDB, g.switchDB, g)
		if settings.ReadSettings(g.baseFolder).OrganizeOptions.DeleteOldUpdateFiles {
			g.sugarLogger.Info("Starting deletion of old updates...")
			process.DeleteOldUpdates(g.baseFolder, g.localDB, g)
		}
		g.sugarLogger.Info("Library organization finished.")
		// Emit an event to signal completion
		runtime.EventsEmit(g.ctx, "organizationComplete", "Library organization finished successfully.")
		// Optionally trigger a library refresh on the frontend
		// runtime.EventsEmit(g.ctx, "refreshLibrary", nil)
	}()

	return nil // Indicate that the process started successfully
}

// IsKeysFileAvailable checks if the keys file seems valid.
func (g *GUI) IsKeysFileAvailable() bool {
	// Consider re-reading keys if they can change, or rely on initial load.
	keys, _ := settings.SwitchKeys() // Use cached keys
	return keys != nil && keys.GetKey("header_key") != ""
}

// LoadSettings reads and returns the current application settings.
func (g *GUI) LoadSettings() (*settings.AppSettings, error) {
	// Reading settings might not need lock, assuming file read is atomic enough
	// or settings are read once at start. If modified elsewhere, use Lock/Unlock.
	s := settings.ReadSettings(g.baseFolder)
	if s == nil {
		return nil, errors.New("failed to read settings")
	}
	return s, nil
}

// SaveSettings saves the provided application settings.
func (g *GUI) SaveSettings(s *settings.AppSettings) error {
	// Saving settings should be protected if reads can happen concurrently.
	g.Lock()
	defer g.Unlock()
	if s == nil {
		return errors.New("invalid settings provided")
	}
	err := settings.SaveSettings(s, g.baseFolder)
	if err != nil {
		g.sugarLogger.Errorf("Failed to save settings: %v", err)
	}
	return err
}

// GetMissingGames identifies games present in the title DB but not locally.
func (g *GUI) GetMissingGames() ([]SwitchTitle, error) {
	g.Lock() // Lock needed as it reads both DBs
	defer g.Unlock()

	if g.localDB == nil || g.switchDB == nil {
		return nil, errors.New("databases not loaded. Please scan library first")
	}

	var result []SwitchTitle
	options := settings.ReadSettings(g.baseFolder) // Read settings inside for latest options

	for k, v := range g.switchDB.TitlesMap {
		if _, ok := g.localDB.TitlesMap[k]; ok {
			continue // Skip if found locally
		}
		if v.Attributes.Name == "" || v.Attributes.Id == "" {
			continue // Skip invalid entries
		}
		if options.HideDemoGames && v.Attributes.IsDemo {
			continue // Skip demos if option is set
		}

		result = append(result, SwitchTitle{
			TitleId:     v.Attributes.Id,
			Name:        v.Attributes.Name,
			Icon:        v.Attributes.BannerUrl, // Use BannerUrl if IconUrl is not the right one
			Region:      v.Attributes.Region,
			ReleaseDate: v.Attributes.ParsedReleaseDate,
		})
	}
	return result, nil
}

// UpdateLocalLibrary scans local files and updates the internal local DB state.
// ignoreCache: If true, forces a rescan ignoring previously cached data.
func (g *GUI) UpdateLocalLibrary(ignoreCache bool) (*LocalLibraryData, error) {
	g.Lock() // Lock needed to modify g.localDB
	defer g.Unlock()

	localDB, err := g.buildLocalDB(g.localDbManager, ignoreCache)
	if err != nil {
		g.sugarLogger.Errorf("Failed to build local DB: %v", err)
		// runtime.EventsEmit(g.ctx, "error", fmt.Sprintf("Failed to scan library: %v", err))
		return nil, fmt.Errorf("failed to scan library: %w", err)
	}

	// Ensure switchDB is loaded before processing local library data fully
	if g.switchDB == nil {
		// Attempt to load it now if not already loaded
		if err := g.UpdateDB(); err != nil {
			// Return partial data or error out? Returning partial data for now.
			g.sugarLogger.Warnf("Switch DB not loaded, library data may be incomplete: %v", err)
			// runtime.EventsEmit(g.ctx, "warning", "Switch DB not loaded, library data may be incomplete.")
		} else {
			g.sugarLogger.Info("Switch DB loaded successfully during local library update.")
		}
	}

	response := LocalLibraryData{}
	libraryData := []LibraryTemplateData{}
	issues := []Pair{}

	for k, v := range localDB.TitlesMap {
		if v.BaseExist {
			version := ""
			name := ""
			icon := ""
			region := ""
			titleId := k // Default to key if no metadata found

			// Try getting info from base file metadata first
			if v.File != nil && v.File.Metadata != nil && v.File.Metadata.Ncap != nil {
				version = v.File.Metadata.Ncap.DisplayVersion
				// Prefer English name if available
				if engTitle, ok := v.File.Metadata.Ncap.TitleName["AmericanEnglish"]; ok {
					name = engTitle.Title
				} else if len(v.File.Metadata.Ncap.TitleName) > 0 {
					// Fallback to the first available language name
					for _, langTitle := range v.File.Metadata.Ncap.TitleName {
						name = langTitle.Title
						break
					}
				}
				titleId = v.File.Metadata.TitleId // Get TitleID from metadata
			}

			// Override with update info if available and newer
			if v.Updates != nil && len(v.Updates) > 0 && v.LatestUpdate > 0 { // Check LatestUpdate > 0
				latestUpdateFile := v.Updates[v.LatestUpdate-1]
				if latestUpdateFile != nil && latestUpdateFile.Metadata != nil && latestUpdateFile.Metadata.Ncap != nil {
					version = latestUpdateFile.Metadata.Ncap.DisplayVersion
					// Update name from update if base name was empty? Usually not needed.
				} else {
					// If update metadata is missing, maybe keep base version or mark as unknown?
					// version = "Unknown Update"
				}
			}

			// Enhance with data from the main Switch DB if available
			if g.switchDB != nil {
				if title, ok := g.switchDB.TitlesMap[k]; ok {
					if title.Attributes.Name != "" {
						name = title.Attributes.Name // Prefer name from central DB
					}
					icon = title.Attributes.IconUrl
					region = title.Attributes.Region
					titleId = title.Attributes.Id // Ensure TitleID from central DB is used
				}
			}

			// Fallback name parsing from filename if still empty
			if name == "" && v.File != nil && v.File.ExtendedInfo != nil {
				name = db.ParseTitleNameFromFileName(v.File.ExtendedInfo.FileName)
			}

			libraryData = append(libraryData,
				LibraryTemplateData{
					// Id:      ?, // How should this ID be generated? Index? Hash?
					Icon:    icon,
					Name:    name,
					TitleId: titleId,
					Update:  v.LatestUpdate,
					Version: version,
					Region:  region,
					Type:    getType(v),
					Path:    filepath.Join(v.File.ExtendedInfo.BaseFolder, v.File.ExtendedInfo.FileName),
					// Dlc:     ?, // How is DLC info determined/represented here?
				})

		} else { // Base file doesn't exist, report issues for related files
			for _, update := range v.Updates {
				issues = append(issues, Pair{Key: filepath.Join(update.ExtendedInfo.BaseFolder, update.ExtendedInfo.FileName), Value: "Base game file is missing"})
			}
			for _, dlc := range v.Dlc {
				issues = append(issues, Pair{Key: filepath.Join(dlc.ExtendedInfo.BaseFolder, dlc.ExtendedInfo.FileName), Value: "Base game file is missing"})
			}
		}
	}
	// Add skipped files to issues
	for k, v := range localDB.Skipped {
		issues = append(issues, Pair{Key: filepath.Join(k.BaseFolder, k.FileName), Value: v.ReasonText})
	}

	response.LibraryData = libraryData
	response.NumFiles = localDB.NumFiles
	response.Issues = issues

	// Emit event instead of returning directly? Depends on how long this takes.
	// For now, returning directly as requested by original logic flow.
	// runtime.EventsEmit(g.ctx, "libraryLoaded", response)
	g.sugarLogger.Infof("Local library updated. Found %d titles, %d issues.", len(libraryData), len(issues))
	return &response, nil
}

// UpdateDB downloads and processes the latest titles/versions databases.
func (g *GUI) UpdateDB() error {
	g.Lock() // Lock needed to modify g.switchDB
	defer g.Unlock()

	// Prevent concurrent updates if one is already running (optional)
	// if g.isUpdatingDB { return errors.New("DB update already in progress") }
	// g.isUpdatingDB = true
	// defer func() { g.isUpdatingDB = false }()

	g.sugarLogger.Info("Starting Switch DB update...")
	switchDb, err := g.buildSwitchDb() // This method handles progress updates
	if err != nil {
		g.sugarLogger.Errorf("Failed to build switch DB: %v", err)
		// runtime.EventsEmit(g.ctx, "error", fmt.Sprintf("Failed to update databases: %v", err))
		return fmt.Errorf("failed to update databases: %w", err)
	}
	g.switchDB = switchDb
	g.sugarLogger.Info("Switch DB update finished successfully.")
	// Emit event to notify frontend DB is updated
	runtime.EventsEmit(g.ctx, "dbUpdated", "Switch databases updated successfully.")
	return nil
}

// GetMissingUpdates identifies installed games that have pending updates.
func (g *GUI) GetMissingUpdates() ([]process.IncompleteTitle, error) {
	g.Lock() // Lock needed as it reads both DBs
	defer g.Unlock()

	if g.localDB == nil || g.switchDB == nil {
		return nil, errors.New("databases not loaded. Please scan library and update DB first")
	}

	settingsObj := settings.ReadSettings(g.baseFolder)
	ignoreIds := map[string]struct{}{}
	for _, id := range settingsObj.IgnoreUpdateTitleIds {
		ignoreIds[strings.ToLower(id)] = struct{}{}
	}

	missingUpdates := process.ScanForMissingUpdates(g.localDB.TitlesMap, g.switchDB.TitlesMap, ignoreIds, settingsObj.IgnoreDLCUpdates)
	// Convert map to slice for returning
	values := make([]process.IncompleteTitle, 0, len(missingUpdates))
	for _, missingUpdate := range missingUpdates {
		values = append(values, missingUpdate)
	}
	return values, nil
}

// GetMissingDLC identifies installed games that have missing DLC.
func (g *GUI) GetMissingDLC() ([]process.IncompleteTitle, error) {
	g.Lock() // Lock needed as it reads both DBs
	defer g.Unlock()

	if g.localDB == nil || g.switchDB == nil {
		return nil, errors.New("databases not loaded. Please scan library and update DB first")
	}

	settingsObj := settings.ReadSettings(g.baseFolder)
	ignoreIds := map[string]struct{}{}
	for _, id := range settingsObj.IgnoreDLCTitleIds {
		ignoreIds[strings.ToLower(id)] = struct{}{}
	}

	missingDLC := process.ScanForMissingDLC(g.localDB.TitlesMap, g.switchDB.TitlesMap, ignoreIds)
	// Convert map to slice for returning
	values := make([]process.IncompleteTitle, 0, len(missingDLC))
	for _, missing := range missingDLC {
		values = append(values, missing)
	}
	return values, nil
}

// CheckForUpdate checks github for a new application release.
func (g *GUI) CheckForUpdate() (bool, error) {
	newUpdate, err := settings.CheckForUpdates()
	if err != nil {
		g.sugarLogger.Errorf("Failed to check for updates: %v", err)
		// Don't emit error event for network issues usually
		// if !strings.Contains(err.Error(), "dial tcp") {
		// 	 runtime.EventsEmit(g.ctx, "error", fmt.Sprintf("Update check failed: %v", err))
		// }
		return false, err // Return error to frontend
	}
	return newUpdate, nil
}

// ClearScanCache clears the local database cache.
func (g *GUI) ClearScanCache() error {
	g.Lock() // May need lock if db manager access isn't thread-safe
	defer g.Unlock()
	g.sugarLogger.Info("Clearing scan cache...")
	err := g.localDbManager.ClearScanData()
	if err == nil {
		g.localDB = nil // Clear in-memory db as well
		g.sugarLogger.Info("Scan cache cleared.")
		runtime.EventsEmit(g.ctx, "cacheCleared", "Scan cache cleared successfully.")
	} else {
		g.sugarLogger.Errorf("Failed to clear scan cache: %v", err)
	}
	return err
}

// --- Helper Methods (Not directly exposed to Javascript) ---

func getType(gameFile *db.SwitchGameFiles) string {
	if gameFile == nil || gameFile.File == nil { // Add nil checks
		return "unknown"
	}
	if gameFile.IsSplit {
		return "split"
	}
	if gameFile.MultiContent {
		return "multi-content"
	}
	ext := filepath.Ext(gameFile.File.ExtendedInfo.FileName)
	if len(ext) > 1 {
		return strings.ToLower(ext[1:]) // Return extension like "nsp", "xci"
	}
	return "unknown"
}

// buildSwitchDb handles the download and processing of title/version dbs.
// It assumes locks are handled by the caller (UpdateDB).
func (g *GUI) buildSwitchDb() (*db.SwitchTitlesDB, error) {
	settingsObj := settings.ReadSettings(g.baseFolder) // Read latest settings

	// 1. load the titles JSON object
	g.UpdateProgress(1, 4, "Downloading titles.json")
	filenameTitles := filepath.Join(g.baseFolder, settings.TITLE_JSON_FILENAME)
	titleFile, titlesEtag, err := db.LoadAndUpdateFile(settingsObj.TitlesJsonUrl, filenameTitles, settingsObj.TitlesEtag)
	if err != nil {
		errMsg := fmt.Sprintf("failed to download/load switch titles [%s]: %v", settingsObj.TitlesJsonUrl, err)
		g.UpdateProgress(1, 4, "Error: "+errMsg) // Update progress with error
		return nil, errors.New(errMsg)
	}
	settingsObj.TitlesEtag = titlesEtag // Update ETag in memory

	// 2. load the versions JSON object
	g.UpdateProgress(2, 4, "Downloading versions.json")
	filenameVersions := filepath.Join(g.baseFolder, settings.VERSIONS_JSON_FILENAME)
	versionsFile, versionsEtag, err := db.LoadAndUpdateFile(settingsObj.VersionsJsonUrl, filenameVersions, settingsObj.VersionsEtag)
	if err != nil {
		errMsg := fmt.Sprintf("failed to download/load switch versions [%s]: %v", settingsObj.VersionsJsonUrl, err)
		g.UpdateProgress(2, 4, "Error: "+errMsg) // Update progress with error
		// Attempt to save the titles ETag even if versions fail
		_ = settings.SaveSettings(settingsObj, g.baseFolder)
		return nil, errors.New(errMsg)
	}
	settingsObj.VersionsEtag = versionsEtag // Update ETag in memory

	// 3. Save settings with potentially updated ETags
	// Save happens *before* processing, so if processing fails, ETags are still saved.
	err = settings.SaveSettings(settingsObj, g.baseFolder)
	if err != nil {
		// Log warning but continue, as files are downloaded.
		g.sugarLogger.Warnf("Failed to save settings with updated ETags: %v", err)
	}

	// 4. Process downloaded files
	g.UpdateProgress(3, 4, "Processing switch titles and updates...")
	switchTitleDB, err := db.CreateSwitchTitleDB(titleFile, versionsFile)
	if err != nil {
		errMsg := fmt.Sprintf("failed to process title/version data: %v", err)
		g.UpdateProgress(3, 4, "Error: "+errMsg) // Update progress with error
		return nil, errors.New(errMsg)
	}

	g.UpdateProgress(4, 4, "Finishing up...")
	return switchTitleDB, nil
}

// buildLocalDB handles scanning local files.
// It assumes locks are handled by the caller (UpdateLocalLibrary).
func (g *GUI) buildLocalDB(localDbManager *db.LocalSwitchDBManager, ignoreCache bool) (*db.LocalSwitchFilesDB, error) {
	settingsObj := settings.ReadSettings(g.baseFolder) // Read latest settings
	folderToScan := settingsObj.Folder
	recursiveMode := settingsObj.ScanRecursively

	if folderToScan == "" && len(settingsObj.ScanFolders) == 0 {
		return nil, errors.New("no scan folder defined in settings")
	}

	scanFolders := settingsObj.ScanFolders
	// Ensure the primary folder is included if defined
	if folderToScan != "" {
		// Avoid duplicates if it's already in ScanFolders
		found := false
		for _, f := range scanFolders {
			if f == folderToScan {
				found = true
				break
			}
		}
		if !found {
			scanFolders = append(scanFolders, folderToScan)
		}
	}

	if len(scanFolders) == 0 {
		return nil, errors.New("no valid scan folders configured")
	}

	g.sugarLogger.Infof("Starting local scan (ignoreCache: %v) in folders: %v", ignoreCache, scanFolders)
	// Reset progress before starting scan
	g.UpdateProgress(0, 1, fmt.Sprintf("Scanning folders: %s...", strings.Join(scanFolders, ", ")))

	localDB, err := localDbManager.CreateLocalSwitchFilesDB(scanFolders, g, recursiveMode, ignoreCache)
	if err != nil {
		g.UpdateProgress(1, 1, "Error during scan.") // Update progress with error
		return nil, fmt.Errorf("failed creating local DB: %w", err)
	}

	g.localDB = localDB                      // Update the main struct's DB reference
	g.UpdateProgress(1, 1, "Scan finished.") // Indicate completion
	g.sugarLogger.Infof("Local scan finished. Found %d files, %d titles, %d skipped.", localDB.NumFiles, len(localDB.TitlesMap), len(localDB.Skipped))
	return localDB, nil
}

// UpdateProgress sends progress updates to the frontend.
// Conforms to the db.ProgressReporter interface.
func (g *GUI) UpdateProgress(curr int, total int, message string) {
	if g.ctx == nil {
		// If context is not yet available (e.g., very early startup), log instead of emitting
		g.sugarLogger.Debugf("Progress (ctx unavailable): %s (%d/%d)", message, curr, total)
		return
	}
	progressMessage := ProgressUpdate{curr, total, message}
	g.sugarLogger.Debugf("Progress: %s (%d/%d)", message, curr, total)
	// Use runtime.EventsEmit to send data to the frontend listener
	runtime.EventsEmit(g.ctx, "updateProgress", progressMessage)
}

// Removed GUI function as its logic is now in Start()
// func GUI() { ... }

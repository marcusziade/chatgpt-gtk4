package app

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	_ "github.com/mattn/go-sqlite3"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/zalando/go-keyring"
)

type App struct {
	*gtk.Application
	win          *gtk.ApplicationWindow
	chatHistory  *gtk.Box
	chatInput    *gtk.TextView
	chatScroll   *gtk.ScrolledWindow
	modelCombo   *gtk.ComboBoxText
	tempScale    *gtk.Scale
	imageDisplay *gtk.Picture
	imagePrompt  *gtk.Entry
	imageSpinner *gtk.Spinner
	statusBar    *gtk.Label
	client       *openai.Client
	db           *sql.DB
}

// Run starts the application
func (a *App) Run() int {
	// Set up cleanup on exit
	a.ConnectShutdown(a.cleanup)

	// Run the application
	return a.Application.Run(os.Args)
}

func New() *App {
	app := &App{
		Application: gtk.NewApplication("com.example.openai-gtk", gio.ApplicationFlagsNone),
	}
	app.Application.ConnectActivate(app.setupUI)
	return app
}

func (a *App) setupUI() {
	if err := a.initDB(); err != nil {
		a.showError("Database Error", err.Error())
		return
	}

	apiKey, err := keyring.Get("com.example.openai-gtk", "api-key")
	if err != nil {
		a.showAPIKeyDialog()
		return
	}

	a.client = openai.NewClient(
		option.WithAPIKey(apiKey),
	)
	a.createMainWindow()
}

func (a *App) initDB() error {
	dbPath := filepath.Join(a.configDir(), "chat_history.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)

	a.db = db
	return err
}

func (a *App) configDir() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return os.TempDir()
	}
	appDir := filepath.Join(configDir, "openai-gtk")
	os.MkdirAll(appDir, 0o755)
	return appDir
}

func (a *App) showAPIKeyDialog() {
	// Create a temporary invisible window to act as parent
	tempWin := gtk.NewApplicationWindow(a.Application)
	tempWin.SetDefaultSize(1, 1) // Minimal size
	tempWin.SetOpacity(0)        // Make it invisible
	tempWin.Show()               // Must show it to be a valid parent

	dialog := gtk.NewDialog()
	dialog.SetTitle("OpenAI API Key")
	dialog.SetModal(true)
	dialog.SetTransientFor(&tempWin.Window)
	dialog.SetDefaultSize(400, 200)

	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.SetMarginTop(16)
	box.SetMarginBottom(16)
	box.SetMarginStart(16)
	box.SetMarginEnd(16)

	label := gtk.NewLabel("Please enter your OpenAI API key:")
	entry := gtk.NewEntry()
	entry.SetVisibility(false)

	button := gtk.NewButtonWithLabel("Save")
	button.ConnectClicked(func() {
		key := entry.Text()
		if err := keyring.Set("com.example.openai-gtk", "api-key", key); err != nil {
			a.showError("Error", "Failed to save API key")
			return
		}
		a.client = openai.NewClient(
			option.WithAPIKey(key),
		)
		dialog.Close()
		tempWin.Close() // Clean up the temporary window
		a.createMainWindow()
	})

	// Also handle dialog close/destroy
	dialog.ConnectDestroy(func() {
		tempWin.Close() // Ensure temporary window is cleaned up
	})

	box.Append(label)
	box.Append(entry)
	box.Append(button)

	dialog.SetChild(box)
	dialog.Show()
}

func (a *App) createMainWindow() {
	a.win = gtk.NewApplicationWindow(a.Application)
	a.win.SetTitle("OpenAI GTK Client")
	a.win.SetDefaultSize(1200, 800)

	// Main layout
	mainBox := gtk.NewBox(gtk.OrientationVertical, 0)

	// Header bar
	header := gtk.NewHeaderBar()
	a.modelCombo = a.createModelSelector()
	a.tempScale = a.createTemperatureControl()
	settingsButton := gtk.NewButtonFromIconName("preferences-system")

	header.PackStart(a.modelCombo)
	header.PackStart(a.tempScale)
	header.PackEnd(settingsButton)
	a.win.SetTitlebar(header)

	// Content area
	paned := gtk.NewPaned(gtk.OrientationHorizontal)
	paned.SetPosition(600)

	// Chat view
	chatBox := a.createChatView()
	paned.SetStartChild(chatBox)

	// Image view
	imageBox := a.createImageView()
	paned.SetEndChild(imageBox)

	mainBox.Append(paned)

	// Status bar
	a.statusBar = gtk.NewLabel("")
	a.statusBar.SetHAlign(gtk.AlignStart)
	a.statusBar.SetMarginStart(8)
	a.statusBar.SetMarginEnd(8)
	a.statusBar.SetMarginTop(4)
	a.statusBar.SetMarginBottom(4)
	mainBox.Append(a.statusBar)

	a.win.SetChild(mainBox)
	a.win.Show()
}

func (a *App) createModelSelector() *gtk.ComboBoxText {
	combo := gtk.NewComboBoxText()
	combo.Append("gpt-4", "GPT-4")
	combo.Append("gpt-3.5-turbo", "GPT-3.5 Turbo")
	combo.SetActive(0)
	return combo
}

func (a *App) createTemperatureControl() *gtk.Scale {
	adjustment := gtk.NewAdjustment(0.7, 0, 2, 0.1, 0.1, 0)
	scale := gtk.NewScale(gtk.OrientationHorizontal, adjustment)
	scale.SetDrawValue(true)
	scale.SetValuePos(gtk.PosRight)
	scale.SetHExpand(true)
	scale.SetSizeRequest(150, -1)
	return scale
}

func (a *App) createChatView() *gtk.Box {
	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.SetMarginTop(8)
	box.SetMarginBottom(8)
	box.SetMarginStart(8)
	box.SetMarginEnd(8)

	// Chat history
	a.chatScroll = gtk.NewScrolledWindow()
	a.chatHistory = gtk.NewBox(gtk.OrientationVertical, 8)
	a.chatScroll.SetChild(a.chatHistory)
	a.chatScroll.SetVExpand(true)

	// Load chat history
	a.loadChatHistory()

	// Input area
	inputBox := gtk.NewBox(gtk.OrientationHorizontal, 8)

	a.chatInput = gtk.NewTextView()
	a.chatInput.SetWrapMode(gtk.WrapWord)
	a.chatInput.SetAcceptsTab(false)
	a.chatInput.SetVExpand(false)
	inputScroll := gtk.NewScrolledWindow()
	inputScroll.SetChild(a.chatInput)
	inputScroll.SetVExpand(false)
	inputScroll.SetSizeRequest(-1, 80)

	sendButton := gtk.NewButtonWithLabel("Send")
	sendButton.ConnectClicked(a.onSendMessage)

	inputBox.Append(inputScroll)
	inputBox.Append(sendButton)

	box.Append(a.chatScroll)
	box.Append(inputBox)

	return box
}

func (a *App) createImageView() *gtk.Box {
	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.SetMarginTop(8)
	box.SetMarginBottom(8)
	box.SetMarginStart(8)
	box.SetMarginEnd(8)

	// Image display
	a.imageDisplay = gtk.NewPicture()
	a.imageDisplay.SetSizeRequest(512, 512)
	a.imageSpinner = gtk.NewSpinner()

	// Prompt input
	a.imagePrompt = gtk.NewEntry()
	a.imagePrompt.SetPlaceholderText("Enter image prompt...")

	generateButton := gtk.NewButtonWithLabel("Generate")
	generateButton.ConnectClicked(a.onGenerateImage)

	// Controls
	controls := gtk.NewBox(gtk.OrientationHorizontal, 8)
	saveButton := gtk.NewButtonWithLabel("Save")
	copyButton := gtk.NewButtonWithLabel("Copy")
	saveButton.ConnectClicked(a.onSaveImage)
	copyButton.ConnectClicked(a.onCopyImage)

	controls.Append(saveButton)
	controls.Append(copyButton)

	box.Append(a.imageDisplay)
	box.Append(a.imagePrompt)
	box.Append(generateButton)
	box.Append(controls)

	return box
}

func (a *App) loadChatHistory() {
	rows, err := a.db.Query("SELECT role, content FROM messages ORDER BY timestamp")
	if err != nil {
		a.setStatus("Error loading chat history")
		return
	}
	defer rows.Close()

	for rows.Next() {
		var role, content string
		if err := rows.Scan(&role, &content); err == nil {
			a.addMessageToUI(role, content)
		}
	}
}

func (a *App) onSendMessage() {
	buffer := a.chatInput.Buffer()
	text := buffer.Text(buffer.StartIter(), buffer.EndIter(), false)
	if text == "" {
		return
	}

	a.addMessageToUI("user", text)
	buffer.SetText("")

	// Save to database
	_, err := a.db.Exec("INSERT INTO messages (role, content) VALUES (?, ?)", "user", text)
	if err != nil {
		a.setStatus("Error saving message")
		return
	}

	// Send to OpenAI
	go func() {
		ctx := context.Background()
		stream := a.client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
			Messages: openai.F([]openai.ChatCompletionMessageParamUnion{
				openai.UserMessage(text),
			}),
			Model: openai.F(openai.ChatModelGPT4o),
		})

		response := ""
		for stream.Next() {
			evt := stream.Current()
			if len(evt.Choices) > 0 {
				response += evt.Choices[0].Delta.Content
				// Update UI in main thread
				glib.IdleAdd(func() {
					a.addMessageToUI("assistant", response)
				})
			}
		}

		if err := stream.Err(); err != nil {
			glib.IdleAdd(func() {
				a.setStatus("API Error: " + err.Error())
			})
			return
		}

		// Save complete response to database
		_, err := a.db.Exec("INSERT INTO messages (role, content) VALUES (?, ?)", "assistant", response)
		if err != nil {
			glib.IdleAdd(func() {
				a.setStatus("Error saving response")
			})
		}
	}()
}

func (a *App) onGenerateImage() {
	prompt := a.imagePrompt.Text()
	if prompt == "" {
		return
	}

	a.imageSpinner.Start()
	a.imageDisplay.SetPaintable(nil) // Clear the current image

	go func() {
		ctx := context.Background()
		result, err := a.client.Images.Generate(ctx, openai.ImageGenerateParams{
			Prompt:         openai.String(prompt),
			Model:          openai.F(openai.ImageModelDallE3),
			ResponseFormat: openai.F(openai.ImageGenerateParamsResponseFormatB64JSON),
			N:              openai.Int(1),
		})

		glib.IdleAdd(func() {
			a.imageSpinner.Stop()

			if err != nil {
				a.setStatus("Image Generation Error: " + err.Error())
				return
			}

			imageBytes, err := base64.StdEncoding.DecodeString(result.Data[0].B64JSON)
			if err != nil {
				a.setStatus("Image Decode Error: " + err.Error())
				return
			}

			tempFile := filepath.Join(a.configDir(), "temp.png")
			if err := os.WriteFile(tempFile, imageBytes, 0o644); err != nil {
				a.setStatus("File Error: " + err.Error())
				return
			}

			texture, err := gdk.NewTextureFromBytes(glib.NewBytesWithGo(imageBytes))
			if err != nil {
				a.setStatus("Error creating texture: " + err.Error())
				return
			}

			a.imageDisplay.SetPaintable(texture)
			a.setStatus("Image generated successfully")
		})
	}()
}

func (a *App) onSaveImage() {
	dialog := gtk.NewFileChooserNative(
		"Save Image",
		&a.win.Window,
		gtk.FileChooserActionSave,
		"_Save",
		"_Cancel",
	)

	// Set up default name and file filter
	dialog.SetCurrentName("generated-image.png")
	filter := gtk.NewFileFilter()
	filter.AddPattern("*.png")
	filter.SetName("PNG images")
	dialog.AddFilter(filter)

	// Set default save location to Pictures directory
	if homeDir, err := os.UserHomeDir(); err == nil {
		picturesDir := filepath.Join(homeDir, "Pictures")
		if _, err := os.Stat(picturesDir); err == nil {
			gfile := gio.NewFileForPath(picturesDir)
			dialog.SetCurrentFolder(gfile)
		}
	}

	// Handle dialog response asynchronously
	responseChan := make(chan int)
	dialog.ConnectResponse(func(response int) {
		responseChan <- response
	})
	dialog.Show()

	go func() {
		response := <-responseChan
		if response == int(gtk.ResponseAccept) {
			file := dialog.File()
			if file == nil {
				glib.IdleAdd(func() {
					a.setStatus("Error: No file selected")
				})
				return
			}

			path := file.Path()
			if !strings.HasSuffix(strings.ToLower(path), ".png") {
				path += ".png"
			}

			// Copy the temporary file to the selected location
			tempFile := filepath.Join(a.configDir(), "temp.png")
			go func() {
				err := a.copyImageFile(tempFile, path)
				glib.IdleAdd(func() {
					if err != nil {
						a.setStatus(fmt.Sprintf("Error saving image: %v", err))
					} else {
						a.setStatus(fmt.Sprintf("Image saved to: %s", path))
					}
				})
			}()
		}
		dialog.Destroy()
	}()
}

func (a *App) copyImageFile(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer srcFile.Close()

	tmpFile, err := os.CreateTemp(filepath.Dir(dest), "*.png")
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		os.Remove(tmpPath)
	}()

	if _, err := io.Copy(tmpFile, srcFile); err != nil {
		return fmt.Errorf("failed to copy image data: %w", err)
	}
	tmpFile.Close()

	if err := os.Rename(tmpPath, dest); err != nil {
		return fmt.Errorf("failed to save image: %w", err)
	}
	return nil
}

func (a *App) onCopyImage() {
	paintable := a.imageDisplay.Paintable()
	if paintable == nil {
		a.setStatus("No image to copy")
		return
	}

	texture, ok := (interface{})(paintable).(gdk.Texturer)
	if !ok {
		a.setStatus("Unable to copy image: invalid format")
		return
	}

	clipboard := gdk.DisplayGetDefault().Clipboard()
	clipboard.SetTexture(texture)
	a.setStatus("Image copied to clipboard")
}

func (a *App) addMessageToUI(role, content string) {
	label := gtk.NewLabel(fmt.Sprintf("%s: %s", role, content))
	label.SetHAlign(gtk.AlignStart)
	label.SetWrap(true)
	label.SetSelectable(true)
	label.SetMarginStart(8)
	label.SetMarginEnd(8)

	a.chatHistory.Append(label)

	// Scroll to bottom
	adjustment := a.chatScroll.VAdjustment()
	adjustment.SetValue(adjustment.Upper())
}

func (a *App) showError(title, message string) {
	// dialog := gtk.NewMessageDialog(
	// 	a.win,
	// 	gtk.DialogModal,
	// 	gtk.MessageError,
	// 	gtk.ButtonsClose,
	// 	message,
	// )
	// dialog.SetTitle(title)
	// dialog.ConnectResponse(func(response int) {
	// 	dialog.Close()
	// })
	// dialog.Show()
}

func (a *App) setStatus(message string) {
	a.statusBar.SetText(message)
}

// Cleanup resources when the application closes
func (a *App) cleanup() {
	if a.db != nil {
		a.db.Close()
	}

	// Clean up temporary files
	tempFile := filepath.Join(a.configDir(), "temp.png")
	os.Remove(tempFile)
}

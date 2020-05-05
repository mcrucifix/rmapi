package annotations

import (
	"bytes"
	"fmt"
 	"errors"
	"os"

	"github.com/juruen/rmapi/archive"
	"github.com/juruen/rmapi/encoding/rm"
	"github.com/juruen/rmapi/log"
	"github.com/unidoc/unipdf/v3/annotator"
	"github.com/unidoc/unipdf/v3/contentstream"
	"github.com/unidoc/unipdf/v3/contentstream/draw"
	"github.com/unidoc/unipdf/v3/core"
	"github.com/unidoc/unipdf/v3/creator"
	pdf "github.com/unidoc/unipdf/v3/model"
)

const (
	DeviceWidth  = 1404
	DeviceHeight = 1872
)

var rmPageSize = creator.PageSize{445, 594}

type PageScale struct {
   scaleX     float64
   scaleY     float64
   shiftX     float64
   shiftY     float64
   rotate  bool
}

type PdfGenerator struct {
	zipName        string
	outputFilePath string
	options        PdfGeneratorOptions
	pdfReader      *pdf.PdfReader
	template       bool
  newcolors      bool // will use red and green if annotating pdf
}

type PdfGeneratorOptions struct {
	AddPageNumbers  bool
	AllPages        bool
	AnnotationsOnly bool //export the annotations without the background/pdf
}

func CreatePdfGenerator(zipName, outputFilePath string, options PdfGeneratorOptions) *PdfGenerator {
	return &PdfGenerator{zipName: zipName, outputFilePath: outputFilePath, options: options}
}

func normalized(p1 rm.Point, pscale PageScale) (float64, float64) {
  if (pscale.rotate) {
  return -float64(p1.Y) * pscale.scaleY + pscale.shiftY , float64(p1.X) * pscale.scaleX - pscale.shiftX
} else {
  return float64(p1.X) * pscale.scaleX + pscale.shiftX , float64(p1.Y) * pscale.scaleY - pscale.shiftY
}
}

func (p *PdfGenerator) Generate() error {
	file, err := os.Open(p.zipName)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	zip := archive.NewZip()

	fi, err := file.Stat()
	if err != nil {
		return err
	}

	err = zip.Read(file, fi.Size())
	if err != nil {
		return err
	}

  orientation := zip.Content.Orientation

	if zip.Content.FileType == "epub" {
		return errors.New("only pdf and notebooks supported")
	}

	if err = p.initBackgroundPages(zip.Payload); err != nil {
		return err
	}

	if len(zip.Pages) == 0 {
		return errors.New("the document has no pages")
	}

	c := creator.New()
	if p.template {
		// use the standard page size
		c.SetPageSize(rmPageSize)
	}

	for i, pageAnnotations := range zip.Pages {
		hasContent := pageAnnotations.Data != nil

		// do not add a page when there are no annotations
		if !p.options.AllPages && !hasContent {
			continue
		}


		page, pscale, err := p.addBackgroundPage(c, orientation, i+1)
		if err != nil {
			return err
		}

		if page == nil {
			log.Error.Fatal("page is null")
		}

		if err != nil {
			return err
		}
		if !hasContent {
			continue
		}

		contentCreator := contentstream.NewContentCreator()
		contentCreator.Add_q()
		contentCreator.Add_j("1")
		contentCreator.Add_J("1")

		for _, layer := range pageAnnotations.Data.Layers {
			for _, line := range layer.Lines {
				if len(line.Points) < 1 {
					continue
				}
				if line.BrushType == rm.Eraser {
					continue
				}
				if line.BrushType == rm.EraseArea {
					continue
				}

				if line.BrushType == rm.HighlighterV5 {
					last := len(line.Points) - 1
					x1, y1 := normalized(line.Points[0], pscale)
					x2, _ := normalized(line.Points[last], pscale)
					// make horizontal lines only, use y1
					width := pscale.scaleX * 30
					y1 += width / 2

					lineDef := annotator.LineAnnotationDef{X1: x1 - 1, Y1: c.Height() - y1, X2: x2, Y2: c.Height() - y1}
					lineDef.LineColor = pdf.NewPdfColorDeviceRGB(1.0, 1.0, 0.0) //yellow
					lineDef.Opacity = 0.5
					lineDef.LineWidth = width
					ann, err := annotator.CreateLineAnnotation(lineDef)
					if err != nil {
						return err
					}
					page.AddAnnotation(ann)
				} else {
					path := draw.NewPath()
					for i := 0; i < len(line.Points); i++ {
						x1, y1 := normalized(line.Points[i], pscale)
						path = path.AppendPoint(draw.NewPoint(x1, c.Height()-y1))
					}
					contentCreator.Add_q()
          //  contentCreator.Add_w(float64(line.BrushSize * 100))
					//  contentCreator.Add_w(float64(.7))
					switch line.BrushSize {
          case rm.Small:
           contentCreator.Add_w(float64(0.5))
         case rm.Medium:
           contentCreator.Add_w(float64(0.5))
         case rm.Large:
           contentCreator.Add_w(float64(0.9))
          }


          if (p.newcolors) {
					switch line.BrushColor {
					case rm.Black:
						contentCreator.Add_RG(0.7, 0.0, 0.0)
					case rm.Grey:
						contentCreator.Add_RG(0.0, 0.7, 0.0)
					}
          } else {
					switch line.BrushColor {
          case rm.Black:
						contentCreator.Add_RG(0.0, 0.0, 0.0)
					case rm.White:
						contentCreator.Add_RG(1.0, 1.0, 1.0)
					case rm.Grey:
						contentCreator.Add_RG(0.7, 0.7, 0.7)
					}
          }

					//TODO: use bezier
					draw.DrawPathWithCreator(path, contentCreator)

					contentCreator.Add_S()
				}
			}
		}
		contentCreator.Add_Q()
		drawingOperations := contentCreator.Operations().String()
		pageContentStreams, err := page.GetAllContentStreams()
		//hack: wrap the page content in a context to prevent transformation matrix misalignment
		wrapper := []string{"q", pageContentStreams, "Q", drawingOperations}
		page.SetContentStreams(wrapper, core.NewFlateEncoder())
	}

	return c.WriteToFile(p.outputFilePath)
}

func (p *PdfGenerator) initBackgroundPages(pdfArr []byte) error {
  p.newcolors = false
  // use black and grey if not annotating a pdf
	if len(pdfArr) > 0 {
		pdfReader, err := pdf.NewPdfReader(bytes.NewReader(pdfArr))
    p.newcolors = true
		if err != nil {
			return err
		}

		p.pdfReader = pdfReader
		p.template = false
		return nil
	}

	p.template = true
	return nil
}

func (p *PdfGenerator) addBackgroundPage(c *creator.Creator, orientation string, pageNum int) (*pdf.PdfPage, PageScale, error) {
	var page *pdf.PdfPage
  var pscale PageScale

  pscale.rotate = (orientation == "landscape")

	if !p.template && !p.options.AnnotationsOnly {
		tmpPage, err := p.pdfReader.GetPage(pageNum)
		if err != nil {
			return nil, pscale, err
		}

    var tbox *pdf.PdfRectangle

    tbox = tmpPage.TrimBox
    cropped := true

    if (tbox == nil) {
      // no trimbox, so no need to crop
      tbox,err = tmpPage.GetMediaBox()
      cropped = false
    }


		// TODO: adjust the page if cropped
	 pageHeight := tbox.Ury  - tbox.Lly
   pageWidth := tbox.Urx  - tbox.Llx
   pscale.shiftX = tbox.Lly
   pscale.shiftY = tbox.Lly

   myDeviceHeight := DeviceHeight
   myDeviceWidth := DeviceWidth

   // to be verified. It seems that it does not rotate in this case
   if (pageHeight > pageWidth) {pscale.rotate = false}

    if (pscale.rotate) {
    myDeviceWidth = DeviceHeight
    myDeviceHeight = DeviceWidth
    }

    h := pageHeight / float64(myDeviceHeight)
    w := pageWidth / float64(myDeviceWidth)


    over := (float64(myDeviceHeight)*w - pageHeight)/2/w
    if (pscale.rotate) {
    over = (float64(myDeviceWidth)*h - pageWidth)/2/h
    }
    if (over < 0) {over = 0}

    scale := w
    if (h > w && !pscale.rotate) { scale = h } // else { scale := w }

    pscale.scaleX = scale
    pscale.scaleY = scale

    if (cropped) {
    tmpPage.TrimBox.Lly -= over/2

    tmpPage.CropBox.Llx = 0
    tmpPage.CropBox.Lly -= over/2

    tmpPage.MediaBox.Llx = 0
    tmpPage.MediaBox.Lly -= over/2
  }
		// use the pdf's page size
		c.SetPageSize(creator.PageSize{pageWidth, pageHeight +over })
		c.AddPage(tmpPage)
    pscale.shiftY -= over
    if (pscale.rotate) {
    pscale.shiftY += pageWidth
    }
		page = tmpPage
	} else {
		c.SetPageSize(creator.PageSize{DeviceWidth,DeviceHeight})
		page = c.NewPage()
    pscale.shiftX = 0
    pscale.shiftY = 0
    pscale.scaleX = 1
    pscale.scaleY = 1
	}

	if p.options.AddPageNumbers {
		c.DrawFooter(func(block *creator.Block, args creator.FooterFunctionArgs) {
			p := c.NewParagraph(fmt.Sprintf("%d", args.PageNum))
			p.SetFontSize(8)
			w := block.Width() - 20
			h := block.Height() - 10
			p.SetPos(w, h)
			block.Draw(p)
		})
	}
	return page, pscale , nil
}

package hclparser

import (
	"3ff/utils"
	"errors"
	"fmt"
	"github.com/hashicorp/hcl2/hcl"
	hclsyntax "github.com/hashicorp/hcl2/hcl/hclsyntax"
	"github.com/hashicorp/hcl2/hclparse"
	"github.com/zclconf/go-cty/cty"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var Debug bool = false

/**
Compare function performs comparison of 2 files, which it receives as arguments, and returns true if there are no diff
o stands for original, m stands for modified
It can compare terrafrom files in HCL2 format only
If arguments are names of directories, it will try to perform File-by-File comparison
*/
func Compare(o, m string) (*ModifiedResources, error) {
	oFile, err := os.Open(o)
	if err != nil {
		log.Fatalf("Cannot open File %s", o)
	}
	defer oFile.Close()
	mFile, err := os.Open(m)
	if err != nil {
		log.Fatalf("Cannot open File %s", m)
	}
	defer mFile.Close()
	return CompareFiles(oFile, mFile)
}

func CompareFiles(o, m *os.File) (*ModifiedResources, error) {
	ofi, err := o.Stat()
	if err != nil {
		if Debug {
			log.Printf("Cannot get File info of %s. Error message: %s", o, err)
		}
		return nil, err
	}
	mfi, err := m.Stat()
	if err != nil {
		if Debug {
			log.Printf("Cannot get File info of %s. Error message: %s", m, err)
		}
		return nil, err
	}
	if ofi.IsDir() != mfi.IsDir() {
		if Debug {
			log.Printf("Both files you specified, should be directories, or both should be files\n%s %s", o, m)
		}
		return nil, errors.New("error: different file types: both files you specified, should be directories, or both should be files")
	}
	mr := NewModifiedResources()
	origFiles, err := getFilesSlice(o)
	if err != nil {
		if Debug {
			log.Printf("Cannot build files list of the directory %s. Error: ", ofi.Name(), err)
		}
		return nil, err
	}
	for _, ofc := range origFiles {
		defer ofc.File.Close()
	}
	modifFiles, err := getFilesSlice(m)
	if err != nil {
		if Debug {
			log.Printf("Cannot build files list of the directory %s. Error: %s", mfi.Name(), err)
		}
		return nil, err
	}
	for _, mfc := range origFiles {
		defer mfc.File.Close()
	}
	ohf, err := getHclFiles(origFiles)
	if err != nil {
		if Debug {
			log.Printf("Cannot parse original files. Error %s", err)
		}
		return nil, err
	}
	mhf, err := getHclFiles(modifFiles)
	if err != nil {
		if Debug {
			log.Printf("Cannot parse modified files %s", err)
		}
		return nil, err
	}
	origCumulativeBody := unpack(ohf)
	modifCumulativeBody := unpack(mhf)

	mr.computeBodyDiff(origCumulativeBody, modifCumulativeBody, nil)
	return mr, err
}

//This function returns a sorted by file name list of files, which was generated by walking through the given directory
func getFilesSlice(root *os.File) (SortableFiles, error) {
	if root == nil {
		return nil, nil
	}
	fileInfo, err := root.Stat()
	if err != nil {
		if Debug {
			log.Printf("Cannot stat File %s", root.Name())
		}
		return nil, err
	}
	if !fileInfo.IsDir() {
		return SortableFiles{SortableFile{File: root}}, nil
	} else {
		var fl SortableFiles
		err := filepath.Walk(root.Name(), func(path string, info os.FileInfo, err error) error {
			if info.IsDir() {
				if strings.HasPrefix(info.Name(), ".") {
					return filepath.SkipDir
				}
			}
			if strings.HasSuffix(path, ".tf") {
				f, err := os.Open(path)
				if err != nil {
					if Debug {
						log.Printf("Cannot open File %s", path)
					}
					return err
				}
				fl = append(fl, SortableFile{File: f})
			}
			return nil
		})
		if err != nil {
			if Debug {
				log.Printf("Cannot walk the directory "+
					"%s tree. Error: %s", root.Name(), err)
			}
			return nil, err
		}
		sort.Sort(fl)
		return fl, nil
	}
}

func getHclFiles(o SortableFiles) ([]*hcl.File, error) {
	var allFiles []*hcl.File = make([]*hcl.File, len(o))
	parser := hclparse.NewParser()
	for i, sf := range o {
		bytes, err := ioutil.ReadAll(sf.File)
		if err != nil {
			log.Fatalf("Cannot read File %s", sf.File.Name())
			return nil, err
		}
		hclFile, diag := parser.ParseHCL(bytes, sf.File.Name())
		if diag != nil && diag.HasErrors() {
			for _, err := range diag.Errs() {
				log.Printf("Cannot parse File %s. Error: %s", sf.File.Name(), err)
				return nil, err
			}
		}
		//By using explicit index I maintain the files order
		allFiles[i] = hclFile
	}
	//NOTE: Perhaps it worth to make diff of the files and output it somehow.
	// Though it is not directly related to the terraform resources
	return allFiles, nil
}

func unpack(hfls []*hcl.File) *Body {
	var atr hclsyntax.Attributes = make(map[string]*hclsyntax.Attribute)
	var hclb hclsyntax.Body = hclsyntax.Body{Attributes: atr, Blocks: make([]*hclsyntax.Block, 0)}
	for _, f := range hfls {
		var hb *hclsyntax.Body = f.Body.(*hclsyntax.Body)
		for k, v := range hb.Attributes {
			if hclb.Attributes[k] != nil {
				if Debug {
					//Check for duplicates
					log.Printf("Cummulative attributes map already contains the value for the key %s", k)
				}
			}
			hclb.Attributes[k] = v
		}
		for _, b := range hb.Blocks {
			hclb.Blocks = append(hclb.Blocks, b)
		}
	}
	b := Body(hclb)
	return &b
}

func (mr *ModifiedResources) computeBodyDiff(ob, mb *Body, path []string) bool {
	oAttrs := NewAttributes(ob.Attributes)
	mAttrs := NewAttributes(mb.Attributes)
	oBlocks := ob.GetBlocks()
	mBlocks := mb.GetBlocks()
	printParams := GetDefaultPrintParams()
	atdf := mr.analyzeAttributesDiff(oAttrs, mAttrs, path, printParams)
	if atdf.HasChanges() {
		PrintModified(strings.Join(path, "/"), printParams)
		printParams.Shift()
		PrintAttributeContext(atdf, printParams)
		printParams.Unshift()
		return false
	}
	return mr.analyzeBlocksDiff(oBlocks, mBlocks, path, GetDefaultPrintParams())
}

//This function returns true if blocks are equal
func (mr *ModifiedResources) computeBlockDiff(o, m *Block, path []string) bool {
	//p := append(path, fmt.Sprintf("%s.%s", o.Type, strings.Join(o.Labels, ".")))
	var pChunk string
	if o.Labels != nil && len(o.Labels) > 0 {
		pChunk = fmt.Sprintf("%s.%s", o.Type, strings.Join(o.Labels, "."))
	} else {
		pChunk = o.Type
	}
	p := append(path, pChunk)
	if o.Type != m.Type {
		if Debug {
			log.Printf("Block types differ. Path: %s\n"+
				"                    Original: %s (in File %s at line: %d, column: %d)\n"+
				"                    Modified: %s (in File %s at line: %d, column: %d)", strings.Join(path, "."),
				o.Type, o.TypeRange.Filename, o.TypeRange.Start.Line, o.TypeRange.Start.Column,
				m.Type, m.TypeRange.Filename, m.TypeRange.Start.Line, m.TypeRange.Start.Column)
			logString, err := utils.GetChangeLogString(o.TypeRange, m.TypeRange)
			if err != nil {
				log.Print("Cannot compose type diff")
			} else {
				log.Println(logString)
			}

		}
		mr.Add(strings.Join(p, "/"))
		return false
	}

	if !mr.computeLabelsDiff(o, m, p) {
		return false
	}

	return mr.computeBodyDiff(o.GetBody(), m.GetBody(), p)

}

//This function returns true if labels are equal
func (mr *ModifiedResources) computeLabelsDiff(o, m *Block, path []string) bool {
	if len(o.Labels) != len(m.Labels) {
		if Debug {

			//Basically this case should never happen
			log.Println("WARNING!!! This should never happen!")
			log.Printf("Lables quantity differ. Path: %s\n"+
				"                    Original: %d (in File %s)\n"+
				"                    Modified: %d (in File %s)", strings.Join(path, "/"),
				len(o.Labels), o.Range().Filename,
				len(m.Labels), m.Range().Filename)
			logString, err := utils.GetChangeLogString(o.Range(), m.Range())
			if err != nil {
				log.Print("Cannot compose labels quantity diff")
			} else {
				log.Println(logString)
			}
		}

		return false
	}
	for i, v := range o.Labels {
		if v != m.Labels[i] {
			if Debug {
				log.Printf("Lables  differ. Path: %s\n"+
					"                    Original: %s (in File %s at line: %d, column: %d)\n"+
					"                    Modified: %s (in File %s at line: %d, column: %d)", strings.Join(path, "/"),
					o.Type, o.LabelRanges[i].Filename, o.LabelRanges[i].Start.Line, o.LabelRanges[i].Start.Column,
					m.Type, m.LabelRanges[i].Filename, m.LabelRanges[i].Start.Line, m.LabelRanges[i].Start.Column)
				logString, err := utils.GetChangeLogString(o.LabelRanges[i], m.LabelRanges[i])
				if err != nil {
					log.Print("Cannot compose label diff")
				} else {
					log.Println(logString)
				}
			}
			mr.Add(strings.Join(path, "/"))
			return false
		}
	}
	return true
}

func expressionEquals(a hclsyntax.Expression, b hclsyntax.Expression) bool {
	if a == nil && b == nil {
		return true
	}
	if ao, ok := a.(*hclsyntax.ObjectConsExpr); ok {
		if bo, ok := b.(*hclsyntax.ObjectConsExpr); ok {
			aem := ao.ExprMap()
			bem := bo.ExprMap()
			log.Println(aem)
			log.Println(bem)
		} else {
			return false
		}

	}

	var aval, bval cty.Value
	if a == nil {
		aval, adiag := a.Value(nil)
		if adiag != nil && adiag.HasErrors() {
			log.Printf("Cannot get value of expression Error: %s", adiag.Error())
		}
		if Debug {
			log.Printf("Expression %s has been removed\n", aval.AsString())
		}
		return false
	}
	if b == nil {
		bval, bdiag := b.Value(nil)
		if bdiag != nil && bdiag.HasErrors() {
			log.Printf("Cannot get value of expression Error: %s", bdiag.Error())
		}
		if Debug {
			log.Printf("Expression %s has been added\n", bval.AsString())
		}
		return false
	}

	//By default it is assumed that object are equal unless a change was detected
	var eq bool = true
	if aval.Type().HasDynamicTypes() {
		eq = aval.RawEquals(bval)
	} else {
		eqval := aval.Equals(bval)
		if !eqval.IsKnown() {
			eq = aval.RawEquals(bval)
		} else {
			eq = eqval.True()
		}
	}
	return eq
}

//TODO: Refactor this for usage in analyze function (This can be simplified)
func attributeEquals(a, b *hclsyntax.Attribute) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil {
		if Debug {
			log.Printf("Attribute %s has been added\n", b.Name)
		}
		return false
	}
	if b == nil {
		if Debug {
			log.Printf("Attribute %s has been removed\n", a.Name)
		}
		return false
	}
	//By default it is assumed that object are equal unless a change was detected
	var eq bool = true

	if a.Name != b.Name {
		if Debug {
			log.Printf("Attribute names differ: %s != %s\n", a.Name, b.Name)
		}
		eq = false
	}
	//TODO: Use expressionEquals here
	av, _ := a.Expr.Value(nil)
	mv, _ := b.Expr.Value(nil)
	if av.Type().HasDynamicTypes() {
		eq = av.RawEquals(mv)
	} else {
		eqval := av.Equals(mv)
		if !eqval.IsKnown() {
			eq = av.RawEquals(mv)
		} else {
			eq = eqval.True()
		}
	}
	return eq
}

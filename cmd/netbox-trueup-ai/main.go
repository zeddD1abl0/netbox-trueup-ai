// Command netbox-trueup-ai keeps NetBox in step with YAML definitions held in
// Git repositories.
//
// The project is in early development: for now the command only reports its
// version.
package main

import (
	"fmt"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/version"
)

func main() {
	fmt.Println("netbox-trueup-ai", version.String())
}

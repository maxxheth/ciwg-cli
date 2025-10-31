package database

import (
	"bufio"
	"io"
	"strings"

	"vitess.io/vitess/go/vt/sqlparser"
)

// OptionsToRemove is the list of WordPress option names to be removed from the database dump.
var OptionsToRemove = []string{
	"license_number",
	"_elementor_pro_license_data",
	"_elementor_pro_license_data_fallback",
	"_elementor_pro_license_v2_data_fallback",
	"_elementor_pro_license_v2_data",
	"_transient_timeout_rg_gforms_license",
	"_transient_rg_gforms_license",
	"_transient_timeout_uael_license_status",
	"_transient_timeout_astra-addon_license_status",
	"astra-addon_license_key",
	"astra_addon_license_key",
	"edd_fs_lock_atomic_wp_rocket",
	"wp_rocket_settings",
}

// Sanitizer provides functionality to clean sensitive data from a SQL dump.
type Sanitizer struct {
	optionsToRemove map[string]struct{}
}

// NewSanitizer creates a new Sanitizer with a predefined list of options to remove.
func NewSanitizer() *Sanitizer {
	optionsMap := make(map[string]struct{})
	for _, opt := range OptionsToRemove {
		optionsMap[opt] = struct{}{}
	}
	return &Sanitizer{optionsToRemove: optionsMap}
}

// Sanitize processes a SQL dump from the reader and writes the cleaned output to the writer.
// It filters out INSERT statements for specific rows in the 'wp_options' table.
func (s *Sanitizer) Sanitize(reader io.Reader, writer io.Writer) error {
	bufferedReader := bufio.NewReader(reader)
	sql := ""

	for {
		line, err := bufferedReader.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}

		sql += line

		// Check if we have a full statement
		if strings.HasSuffix(strings.TrimSpace(line), ";") {
			stmt, err := sqlparser.Parse(sql)
			if err != nil {
				// This might not be a valid statement on its own, could be part of a larger one.
				// Or it's just not something the parser understands. Write it and continue.
				if _, writeErr := writer.Write([]byte(sql)); writeErr != nil {
					return writeErr
				}
				sql = "" // Reset for next statement
				if err == io.EOF {
					break
				}
				continue
			}

			keepStatement := true
			switch stmt := stmt.(type) {
			case *sqlparser.Insert:
				// Check if it's an insert into wp_options
				if strings.Contains(stmt.Table.Name.String(), "options") {
					rows, ok := stmt.Rows.(sqlparser.Values)
					if !ok {
						// Not a VALUES clause, keep it
						break
					}

					// Check each row in the INSERT statement
					for _, row := range rows {
						if len(row) > 1 {
							// option_name is typically the second value in the tuple
							optionNameVal, ok := row[1].(*sqlparser.SQLVal)
							if !ok {
								continue
							}
							optionName := string(optionNameVal.Val)

							// If the option name is in our removal list, we mark the statement for deletion
							if _, exists := s.optionsToRemove[optionName]; exists {
								keepStatement = false
								break // No need to check other rows in this statement
							}
						}
					}
				}
			}

			if keepStatement {
				if _, err := writer.Write([]byte(sql)); err != nil {
					return err
				}
			}

			sql = "" // Reset for next statement
		}

		if err == io.EOF {
			break
		}
	}

	// Write any remaining sql that wasn't a complete statement
	if sql != "" {
		if _, err := writer.Write([]byte(sql)); err != nil {
			return err
		}
	}

	return nil
}

// Package migrate implementa el runner propio de migraciones SQL (ADR-020).
//
// Las migraciones son parejas de ficheros escritos a mano con la convención
// NNNN_nombre.up.sql / NNNN_nombre.down.sql (NNNN secuencial de 4 dígitos
// empezando en 0001, sin huecos ni duplicados). El runner mantiene la tabla
// de control public.schema_migrations, aplica cada migración dentro de su
// propia transacción y detecta drift verificando el checksum SHA-256 del
// fichero up de cada migración aplicada.
//
// La directiva "-- migrate:no-transaction" en la primera línea de un fichero
// (up o down, cada uno por separado) lo excluye de la transacción: sus
// sentencias se ejecutan de una en una en modo autocommit, necesario para
// CREATE INDEX CONCURRENTLY y similares, que PostgreSQL rechaza dentro de un
// bloque de transacción (incluida la transacción implícita de un mensaje
// simple-query multisentencia).
//
// Sin dependencias más allá de pgx (ADR-017).
package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// NoTxDirective, en la primera línea de un fichero .sql, excluye su ejecución
// de la transacción implícita del runner.
const NoTxDirective = "-- migrate:no-transaction"

const (
	upSuffix   = ".up.sql"
	downSuffix = ".down.sql"
)

var (
	// fileRE valida la base del fichero (sin sufijo): NNNN_nombre.
	fileRE = regexp.MustCompile(`^(\d{4})_([a-z][a-z0-9_]*)$`)
	// nameRE valida el nombre de una migración nueva (Create).
	nameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// Migration es una pareja up/down descubierta en el directorio de migraciones.
type Migration struct {
	// Version es el número secuencial NNNN (1..9999).
	Version int
	// Name es el nombre en snake_case tras el prefijo NNNN_.
	Name string
	// UpPath y DownPath son las rutas de los ficheros de la pareja.
	UpPath   string
	DownPath string
	// UpSQL y DownSQL son los contenidos completos de los ficheros.
	UpSQL   string
	DownSQL string
	// Checksum es el SHA-256 (hex) del fichero up, tal cual está en disco.
	Checksum string
	// NoTxUp y NoTxDown indican la directiva no-transaction en cada fichero.
	NoTxUp   bool
	NoTxDown bool
}

// ID devuelve el identificador canónico NNNN_nombre de la migración.
func (m Migration) ID() string { return fmt.Sprintf("%04d_%s", m.Version, m.Name) }

// Discover lee el directorio y devuelve las migraciones ordenadas por
// versión. Es estricto con la convención: toda pareja debe estar completa,
// las versiones deben ser consecutivas desde 0001 y cualquier fichero .sql
// que no cumpla el patrón NNNN_nombre.{up,down}.sql es un error. Los ficheros
// que no terminan en .sql se ignoran.
func Discover(dir string) ([]Migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("leyendo el directorio de migraciones: %w", err)
	}
	byVersion := make(map[int]*Migration)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, name, isUp, err := parseFilename(e.Name())
		if err != nil {
			return nil, err
		}
		m := byVersion[version]
		if m == nil {
			m = &Migration{Version: version, Name: name}
			byVersion[version] = m
		} else if m.Name != name {
			return nil, fmt.Errorf("versión %04d duplicada: %q y %q", version, m.Name, name)
		}
		if isUp {
			m.UpPath = filepath.Join(dir, e.Name())
		} else {
			m.DownPath = filepath.Join(dir, e.Name())
		}
	}

	versions := make([]int, 0, len(byVersion))
	for v := range byVersion {
		versions = append(versions, v)
	}
	sort.Ints(versions)

	migs := make([]Migration, 0, len(versions))
	for i, v := range versions {
		if v != i+1 {
			return nil, fmt.Errorf("hueco en la numeración de migraciones: esperada la versión %04d y encontrada %04d", i+1, v)
		}
		m := byVersion[v]
		if m.UpPath == "" {
			return nil, fmt.Errorf("la migración %04d_%s no tiene fichero %s", m.Version, m.Name, upSuffix)
		}
		if m.DownPath == "" {
			return nil, fmt.Errorf("la migración %04d_%s no tiene fichero %s", m.Version, m.Name, downSuffix)
		}
		upBytes, err := os.ReadFile(m.UpPath)
		if err != nil {
			return nil, fmt.Errorf("leyendo %s: %w", m.UpPath, err)
		}
		downBytes, err := os.ReadFile(m.DownPath)
		if err != nil {
			return nil, fmt.Errorf("leyendo %s: %w", m.DownPath, err)
		}
		m.UpSQL = string(upBytes)
		m.DownSQL = string(downBytes)
		m.Checksum = checksumOf(upBytes)
		m.NoTxUp = hasNoTxDirective(m.UpSQL)
		m.NoTxDown = hasNoTxDirective(m.DownSQL)
		migs = append(migs, *m)
	}
	return migs, nil
}

// Create escribe la pareja de ficheros de la siguiente versión libre con una
// cabecera comentada y devuelve las rutas creadas. El nombre debe ser
// snake_case en minúsculas y empezar por letra.
func Create(dir, name string) (upPath, downPath string, err error) {
	if !nameRE.MatchString(name) {
		return "", "", fmt.Errorf("nombre de migración inválido %q (formato: minúsculas, dígitos y _, empezando por letra)", name)
	}
	migs, err := Discover(dir)
	if err != nil {
		return "", "", err
	}
	next := len(migs) + 1
	if next > 9999 {
		return "", "", fmt.Errorf("agotada la numeración de 4 dígitos (%d migraciones)", len(migs))
	}
	id := fmt.Sprintf("%04d_%s", next, name)
	upPath = filepath.Join(dir, id+upSuffix)
	downPath = filepath.Join(dir, id+downSuffix)

	upBody := header(id, "up", "Describe aquí el cambio de esquema y su motivación.")
	downBody := header(id, "down", fmt.Sprintf("Revierte %s en orden inverso a su creación.", id))
	if err := os.WriteFile(upPath, []byte(upBody), 0o644); err != nil {
		return "", "", fmt.Errorf("escribiendo %s: %w", upPath, err)
	}
	if err := os.WriteFile(downPath, []byte(downBody), 0o644); err != nil {
		return "", "", fmt.Errorf("escribiendo %s: %w", downPath, err)
	}
	return upPath, downPath, nil
}

// header genera la cabecera comentada de un fichero de migración nuevo,
// coherente con el estilo de las migraciones existentes del proyecto.
func header(id, kind, desc string) string {
	const rule = "-- ============================================================================="
	return fmt.Sprintf("%s\n-- Imperio Industrial — %s (%s)\n-- %s\n%s\n\n", rule, id, kind, desc, rule)
}

// parseFilename separa versión, nombre y tipo (up/down) de un fichero .sql.
func parseFilename(fn string) (version int, name string, isUp bool, err error) {
	base := fn
	switch {
	case strings.HasSuffix(base, upSuffix):
		isUp = true
		base = strings.TrimSuffix(base, upSuffix)
	case strings.HasSuffix(base, downSuffix):
		base = strings.TrimSuffix(base, downSuffix)
	default:
		return 0, "", false, fmt.Errorf("fichero %q: los .sql del directorio deben terminar en %s o %s", fn, upSuffix, downSuffix)
	}
	m := fileRE.FindStringSubmatch(base)
	if m == nil {
		return 0, "", false, fmt.Errorf("fichero %q: no cumple la convención NNNN_nombre.{up,down}.sql", fn)
	}
	version, err = strconv.Atoi(m[1])
	if err != nil {
		return 0, "", false, fmt.Errorf("fichero %q: versión ilegible: %w", fn, err)
	}
	if version == 0 {
		return 0, "", false, fmt.Errorf("fichero %q: la versión 0000 no es válida (la numeración empieza en 0001)", fn)
	}
	return version, m[2], isUp, nil
}

// checksumOf devuelve el SHA-256 en hexadecimal de los bytes dados.
func checksumOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// hasNoTxDirective detecta la directiva no-transaction en la primera línea.
func hasNoTxDirective(sql string) bool {
	line := sql
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line) == NoTxDirective
}

// splitStatements divide un script SQL en sentencias individuales para su
// ejecución fuera de transacción: PostgreSQL envuelve un mensaje simple-query
// multisentencia en una transacción implícita, lo que rompería justamente los
// CREATE/DROP INDEX CONCURRENTLY que motivan la directiva. Reconoce
// comentarios de línea (--) y de bloque (/* */, anidados), cadenas entre
// comillas simples e identificadores entre comillas dobles (ambos con la
// comilla duplicada como escape) y dollar-quoting ($$ o $tag$). Descarta
// las sentencias vacías.
func splitStatements(sql string) []string {
	var stmts []string
	var b strings.Builder
	flush := func() {
		if s := strings.TrimSpace(b.String()); s != "" {
			stmts = append(stmts, s)
		}
		b.Reset()
	}
	i, n := 0, len(sql)
	for i < n {
		c := sql[i]
		switch {
		case c == '-' && i+1 < n && sql[i+1] == '-':
			// Comentario de línea: hasta el fin de línea inclusive.
			if j := strings.IndexByte(sql[i:], '\n'); j >= 0 {
				b.WriteString(sql[i : i+j+1])
				i += j + 1
			} else {
				b.WriteString(sql[i:])
				i = n
			}
		case c == '/' && i+1 < n && sql[i+1] == '*':
			// Comentario de bloque, anidable según PostgreSQL.
			depth, j := 1, i+2
			for j < n && depth > 0 {
				switch {
				case sql[j] == '*' && j+1 < n && sql[j+1] == '/':
					depth--
					j += 2
				case sql[j] == '/' && j+1 < n && sql[j+1] == '*':
					depth++
					j += 2
				default:
					j++
				}
			}
			b.WriteString(sql[i:j])
			i = j
		case c == '\'' || c == '"':
			// Literal o identificador entrecomillado; la comilla doblada escapa.
			j := i + 1
			for j < n {
				if sql[j] == c {
					if j+1 < n && sql[j+1] == c {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			b.WriteString(sql[i:j])
			i = j
		case c == '$':
			tag, ok := dollarTag(sql[i:])
			if !ok {
				b.WriteByte(c)
				i++
				continue
			}
			// Cuerpo dollar-quoted: hasta la repetición del delimitador.
			rest := sql[i+len(tag):]
			if end := strings.Index(rest, tag); end >= 0 {
				stop := i + len(tag) + end + len(tag)
				b.WriteString(sql[i:stop])
				i = stop
			} else {
				b.WriteString(sql[i:])
				i = n
			}
		case c == ';':
			flush()
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	flush()
	return stmts
}

// dollarTag devuelve el delimitador $tag$ (o $$) si s comienza por uno
// válido: el tag sigue las reglas de un identificador sin comillas.
func dollarTag(s string) (string, bool) {
	if len(s) < 2 || s[0] != '$' {
		return "", false
	}
	if s[1] == '$' {
		return "$$", true
	}
	if !isIdentStart(s[1]) {
		return "", false
	}
	for j := 2; j < len(s); j++ {
		switch {
		case s[j] == '$':
			return s[:j+1], true
		case !isIdentPart(s[j]):
			return "", false
		}
	}
	return "", false
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

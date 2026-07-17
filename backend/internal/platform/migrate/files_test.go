package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write crea un fichero en dir con el contenido dado.
func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("escribiendo %s: %v", name, err)
	}
}

// writePair crea la pareja up/down de una migración con contenido trivial.
func writePair(t *testing.T, dir, id string) {
	t.Helper()
	write(t, dir, id+".up.sql", "SELECT 'up "+id+"';\n")
	write(t, dir, id+".down.sql", "SELECT 'down "+id+"';\n")
}

func TestDiscoverOrderAndContent(t *testing.T) {
	dir := t.TempDir()
	// Creación deliberadamente desordenada.
	writePair(t, dir, "0002_beta")
	writePair(t, dir, "0001_alfa")
	writePair(t, dir, "0003_gamma")
	write(t, dir, "README.md", "no soy una migración") // ignorado: no es .sql

	migs, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(migs) != 3 {
		t.Fatalf("len = %d, esperado 3", len(migs))
	}
	wantNames := []string{"alfa", "beta", "gamma"}
	for i, m := range migs {
		if m.Version != i+1 || m.Name != wantNames[i] {
			t.Errorf("migs[%d] = %04d_%s, esperado %04d_%s", i, m.Version, m.Name, i+1, wantNames[i])
		}
		if m.UpSQL == "" || m.DownSQL == "" {
			t.Errorf("%s: contenido up/down vacío", m.ID())
		}
		if m.UpPath == "" || m.DownPath == "" {
			t.Errorf("%s: rutas vacías", m.ID())
		}
	}
	if migs[0].ID() != "0001_alfa" {
		t.Errorf("ID() = %q, esperado 0001_alfa", migs[0].ID())
	}
}

func TestDiscoverEmptyDir(t *testing.T) {
	migs, err := Discover(t.TempDir())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(migs) != 0 {
		t.Fatalf("len = %d, esperado 0", len(migs))
	}
}

func TestDiscoverMissingDir(t *testing.T) {
	if _, err := Discover(filepath.Join(t.TempDir(), "no_existe")); err == nil {
		t.Fatal("Discover sin error con directorio inexistente")
	}
}

func TestDiscoverGap(t *testing.T) {
	dir := t.TempDir()
	writePair(t, dir, "0001_a")
	writePair(t, dir, "0003_c")
	_, err := Discover(dir)
	if err == nil || !strings.Contains(err.Error(), "0002") {
		t.Fatalf("esperado error de hueco mencionando 0002, obtenido: %v", err)
	}
}

func TestDiscoverMustStartAtOne(t *testing.T) {
	dir := t.TempDir()
	writePair(t, dir, "0002_b")
	_, err := Discover(dir)
	if err == nil || !strings.Contains(err.Error(), "0001") {
		t.Fatalf("esperado error de hueco mencionando 0001, obtenido: %v", err)
	}
}

func TestDiscoverDuplicateVersion(t *testing.T) {
	dir := t.TempDir()
	writePair(t, dir, "0001_a")
	writePair(t, dir, "0001_b")
	_, err := Discover(dir)
	if err == nil || !strings.Contains(err.Error(), "duplicada") {
		t.Fatalf("esperado error de versión duplicada, obtenido: %v", err)
	}
}

func TestDiscoverMissingHalf(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "0001_solo_up.up.sql", "SELECT 1;\n")
	_, err := Discover(dir)
	if err == nil || !strings.Contains(err.Error(), ".down.sql") {
		t.Fatalf("esperado error por .down.sql ausente, obtenido: %v", err)
	}

	dir2 := t.TempDir()
	write(t, dir2, "0001_solo_down.down.sql", "SELECT 1;\n")
	_, err = Discover(dir2)
	if err == nil || !strings.Contains(err.Error(), ".up.sql") {
		t.Fatalf("esperado error por .up.sql ausente, obtenido: %v", err)
	}
}

func TestDiscoverBadFilenames(t *testing.T) {
	for _, fn := range []string{
		"0001-init.up.sql",  // separador incorrecto
		"01_init.up.sql",    // versión de 2 dígitos
		"0001_Init.up.sql",  // mayúscula en el nombre
		"0001_1init.up.sql", // nombre que empieza por dígito
		"init.sql",          // sin sufijo up/down
		"0000_cero.up.sql",  // versión 0000
	} {
		dir := t.TempDir()
		write(t, dir, fn, "SELECT 1;\n")
		if _, err := Discover(dir); err == nil {
			t.Errorf("Discover aceptó el fichero inválido %q", fn)
		}
	}
}

func TestChecksumStableAndSensitive(t *testing.T) {
	dir := t.TempDir()
	content := "CREATE TABLE t (id INT);\n"
	write(t, dir, "0001_t.up.sql", content)
	write(t, dir, "0001_t.down.sql", "DROP TABLE t;\n")

	sum := sha256.Sum256([]byte(content))
	want := hex.EncodeToString(sum[:])

	migs, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if migs[0].Checksum != want {
		t.Errorf("checksum = %s, esperado %s", migs[0].Checksum, want)
	}

	// Estable: una segunda lectura produce el mismo checksum.
	again, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if again[0].Checksum != migs[0].Checksum {
		t.Error("checksum inestable entre lecturas")
	}

	// Sensible: cambiar el fichero up cambia el checksum.
	write(t, dir, "0001_t.up.sql", content+"-- cambio\n")
	changed, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if changed[0].Checksum == migs[0].Checksum {
		t.Error("el checksum no cambió al modificar el fichero up")
	}
}

func TestNoTxDirective(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{"-- migrate:no-transaction\nSELECT 1;", true},
		{"-- migrate:no-transaction\r\nSELECT 1;", true},
		{"  -- migrate:no-transaction  \nSELECT 1;", true},
		{"-- migrate:no-transaction", true},              // sin salto de línea final
		{"SELECT 1;\n-- migrate:no-transaction", false},  // no está en la primera línea
		{"-- migrate: no-transaction\nSELECT 1;", false}, // espacio de más
		{"SELECT 1;", false},
		{"", false},
	}
	for _, c := range cases {
		if got := hasNoTxDirective(c.sql); got != c.want {
			t.Errorf("hasNoTxDirective(%q) = %v, esperado %v", c.sql, got, c.want)
		}
	}
}

func TestDiscoverSetsNoTxFlags(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "0001_idx.up.sql", NoTxDirective+"\nCREATE INDEX CONCURRENTLY i ON t (c);\n")
	write(t, dir, "0001_idx.down.sql", "DROP INDEX i;\n")
	migs, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !migs[0].NoTxUp {
		t.Error("NoTxUp = false, esperado true")
	}
	if migs[0].NoTxDown {
		t.Error("NoTxDown = true, esperado false")
	}
}

func TestCreate(t *testing.T) {
	dir := t.TempDir()

	up, down, err := Create(dir, "inicial")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if filepath.Base(up) != "0001_inicial.up.sql" || filepath.Base(down) != "0001_inicial.down.sql" {
		t.Fatalf("rutas inesperadas: %s / %s", up, down)
	}
	for _, p := range []string{up, down} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("leyendo %s: %v", p, err)
		}
		if !strings.Contains(string(b), "0001_inicial") {
			t.Errorf("%s: la cabecera no menciona 0001_inicial", p)
		}
	}

	// La siguiente versión es 0002 y lo creado pasa Discover.
	up2, _, err := Create(dir, "segunda")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if filepath.Base(up2) != "0002_segunda.up.sql" {
		t.Fatalf("segunda versión = %s, esperado 0002_segunda.up.sql", filepath.Base(up2))
	}
	migs, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover tras Create: %v", err)
	}
	if len(migs) != 2 {
		t.Fatalf("len = %d, esperado 2", len(migs))
	}
}

func TestCreateRejectsInvalidNames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"", "Mayuscula", "1numero", "con espacio", "con-guion", "acentuación"} {
		if _, _, err := Create(dir, name); err == nil {
			t.Errorf("Create aceptó el nombre inválido %q", name)
		}
	}
}

func TestCreateFailsOnInvalidDir(t *testing.T) {
	dir := t.TempDir()
	writePair(t, dir, "0002_huerfana") // hueco: falta 0001
	if _, _, err := Create(dir, "nueva"); err == nil {
		t.Fatal("Create sin error sobre un directorio inválido")
	}
}

func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{"simple", "SELECT 1; SELECT 2;", []string{"SELECT 1", "SELECT 2"}},
		{"sin punto y coma final", "SELECT 1", []string{"SELECT 1"}},
		{"vacías descartadas", "SELECT 1;;\n ;", []string{"SELECT 1"}},
		{"comentario de línea", "-- a; b\nSELECT 1;", []string{"-- a; b\nSELECT 1"}},
		{"comentario de bloque", "/* a; b */ SELECT 1;", []string{"/* a; b */ SELECT 1"}},
		{"bloque anidado", "/* a /* b; */ c; */ SELECT 1;", []string{"/* a /* b; */ c; */ SELECT 1"}},
		{"cadena", "SELECT 'a;''b'; SELECT 2;", []string{"SELECT 'a;''b'", "SELECT 2"}},
		{"identificador", `CREATE TABLE "a;b" (id INT);`, []string{`CREATE TABLE "a;b" (id INT)`}},
		{
			"dollar anónimo",
			"CREATE FUNCTION f() RETURNS void AS $$ BEGIN PERFORM 1; END; $$ LANGUAGE plpgsql; SELECT 1;",
			[]string{"CREATE FUNCTION f() RETURNS void AS $$ BEGIN PERFORM 1; END; $$ LANGUAGE plpgsql", "SELECT 1"},
		},
		{
			"dollar etiquetado",
			"SELECT $fn$uno; dos$fn$; SELECT 2;",
			[]string{"SELECT $fn$uno; dos$fn$", "SELECT 2"},
		},
		{"dólar suelto no es tag", "SELECT 1 ; SELECT '$'||x;", []string{"SELECT 1", "SELECT '$'||x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitStatements(c.sql)
			if len(got) != len(c.want) {
				t.Fatalf("len = %d, esperado %d (%q)", len(got), len(c.want), got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("stmt[%d] = %q, esperado %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestVerifyAppliedState(t *testing.T) {
	migs := []Migration{
		{Version: 1, Name: "a", Checksum: "aaa"},
		{Version: 2, Name: "b", Checksum: "bbb"},
	}
	ok := []Record{{Version: 1, Name: "a", Checksum: "aaa"}}
	if err := verifyAppliedState(migs, ok, true); err != nil {
		t.Errorf("estado limpio con error: %v", err)
	}

	drift := []Record{{Version: 1, Name: "a", Checksum: "zzz"}}
	if err := verifyAppliedState(migs, drift, true); err == nil || !strings.Contains(err.Error(), "drift") {
		t.Errorf("drift de checksum no detectado: %v", err)
	}
	// Sin verificación de checksums (vía de recuperación de Down) no es error.
	if err := verifyAppliedState(migs, drift, false); err != nil {
		t.Errorf("checkChecksums=false no debería fallar por checksum: %v", err)
	}

	badName := []Record{{Version: 1, Name: "otra", Checksum: "aaa"}}
	if err := verifyAppliedState(migs, badName, false); err == nil {
		t.Error("discrepancia de nombre no detectada")
	}

	nonPrefix := []Record{{Version: 2, Name: "b", Checksum: "bbb"}}
	if err := verifyAppliedState(migs, nonPrefix, true); err == nil {
		t.Error("historial no consecutivo no detectado")
	}

	orphan := []Record{
		{Version: 1, Name: "a", Checksum: "aaa"},
		{Version: 2, Name: "b", Checksum: "bbb"},
		{Version: 3, Name: "c", Checksum: "ccc"},
	}
	if err := verifyAppliedState(migs, orphan, true); err == nil {
		t.Error("registro huérfano (sin fichero) no detectado")
	}
}

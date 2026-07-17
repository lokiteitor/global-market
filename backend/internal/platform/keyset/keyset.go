// Package keyset codifica cursores de paginación keyset opacos
// (meta.next_cursor del contrato): la clave de ordenación de la última fila
// devuelta se serializa en binario de anchura fija + base64url sin padding y
// el cliente la trata como un string opaco.
//
// La API es genérica: Encode y Decode operan sobre un struct pequeño cuyos
// campos exportados, en su orden de declaración, forman la clave de
// ordenación. Tipos de campo soportados:
//
//   - uuid.UUID → 16 bytes.
//   - time.Time → 8 bytes (µs Unix, big-endian). timestamptz almacena
//     microsegundos, por lo que la ida y vuelta es exacta y la comparación
//     keyset no pierde filas; se decodifica en UTC.
//   - int64 y tipos derivados (p. ej. simtime.SimTime) → 8 bytes big-endian.
//
// La anchura fija hace que un cursor de una forma nunca valga para otra de
// longitud distinta: Decode lo rechaza con ErrInvalidCursor. Una forma no
// soportada (struct sin campos, campos no exportados o de otro tipo) es un
// error de programación, no de datos: Encode y Decode entran en pánico.
package keyset

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
)

// ErrInvalidCursor: el string no es un cursor emitido por Encode para esta
// forma (base64url inválido o longitud distinta). Los módulos lo envuelven en
// su propio error tipado (→ 400 VALIDATION_ERROR).
var ErrInvalidCursor = errors.New("keyset: cursor de paginación inválido")

// Anchuras en bytes de cada tipo de campo soportado.
const (
	widthUUID  = 16
	widthInt64 = 8
)

var (
	uuidType = reflect.TypeOf(uuid.UUID{})
	timeType = reflect.TypeOf(time.Time{})
)

// Encode serializa los campos de cursor, en orden de declaración, a un cursor
// opaco base64url sin padding.
func Encode[T any](cursor T) string {
	rv := reflect.ValueOf(cursor)
	raw := make([]byte, 0, encodedLen(rv.Type()))
	for i := range rv.NumField() {
		f := rv.Field(i)
		switch {
		case f.Type() == uuidType:
			id := f.Interface().(uuid.UUID)
			raw = append(raw, id[:]...)
		case f.Type() == timeType:
			at := f.Interface().(time.Time)
			raw = binary.BigEndian.AppendUint64(raw, uint64(at.UnixMicro()))
		default: // int64 (validado por encodedLen)
			raw = binary.BigEndian.AppendUint64(raw, uint64(f.Int()))
		}
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// Decode valida y deserializa un cursor emitido por Encode con la misma forma
// T. Cualquier entrada malformada (base64url inválido, longitud distinta a la
// de T) devuelve ErrInvalidCursor.
func Decode[T any](s string) (T, error) {
	var cursor T
	rv := reflect.ValueOf(&cursor).Elem()
	want := encodedLen(rv.Type())

	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	if len(raw) != want {
		return cursor, fmt.Errorf("%w: longitud %d, esperados %d bytes", ErrInvalidCursor, len(raw), want)
	}

	off := 0
	for i := range rv.NumField() {
		f := rv.Field(i)
		switch {
		case f.Type() == uuidType:
			var id uuid.UUID
			copy(id[:], raw[off:off+widthUUID])
			f.Set(reflect.ValueOf(id))
			off += widthUUID
		case f.Type() == timeType:
			us := int64(binary.BigEndian.Uint64(raw[off : off+widthInt64]))
			f.Set(reflect.ValueOf(time.UnixMicro(us).UTC()))
			off += widthInt64
		default: // int64 (validado por encodedLen)
			f.SetInt(int64(binary.BigEndian.Uint64(raw[off : off+widthInt64])))
			off += widthInt64
		}
	}
	return cursor, nil
}

// encodedLen valida la forma del cursor y devuelve su anchura total en bytes.
// Entra en pánico ante formas no soportadas: es un bug del módulo llamador,
// nunca una condición de runtime.
func encodedLen(rt reflect.Type) int {
	if rt.Kind() != reflect.Struct {
		panic(fmt.Sprintf("keyset: %s no es un struct", rt))
	}
	if rt.NumField() == 0 {
		panic(fmt.Sprintf("keyset: %s no tiene campos", rt))
	}
	total := 0
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			panic(fmt.Sprintf("keyset: el campo %s.%s no es exportado", rt, f.Name))
		}
		switch {
		case f.Type == uuidType:
			total += widthUUID
		case f.Type == timeType, f.Type.Kind() == reflect.Int64:
			total += widthInt64
		default:
			panic(fmt.Sprintf("keyset: tipo no soportado %s en el campo %s.%s", f.Type, rt, f.Name))
		}
	}
	return total
}

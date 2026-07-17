// ESLint flat config — @nuxt/eslint + reglas de fronteras del FAD §23.3.
// Violación de frontera = build roto: esta es la garantía mecánica de que la
// arquitectura no se erosiona.
import withNuxt from './.nuxt/eslint.config.mjs'

/**
 * Identificadores "monetarios" para la regla anti-float (C11), mejor esfuerzo:
 * nombres que contengan money/amount/price/quantity/qty en camelCase o
 * snake_case. esquery no soporta flags de regex de forma portable, así que las
 * variantes de mayúscula se enumeran explícitamente.
 */
const MONEY_IDENT = '(money|Money|amount|Amount|price|Price|quantity|Quantity|qty|Qty)'

/**
 * Regla anti-float sobre dinero/cantidades (FAD C11/§20.6), mejor esfuerzo:
 * detecta `parseFloat(x)`, `Number(x)`, `parseInt(x)`, `Number.parseFloat(x)`,
 * `Number.parseInt(x)` y el unario `+x` cuando el argumento es un identificador
 * o propiedad cuyo nombre sugiere Money/Quantity. Limitación documentada: es
 * análisis sintáctico (no de tipos) — no ve alias opacos (`const v = offer.price;
 * Number(v)` con `v` sin nombre monetario) ni llamadas indirectas. La defensa
 * de fondo son los branded types de shared/money (la aritmética solo existe
 * sobre BigInt) y la revisión de código.
 */
const moneyFloatSelectors = [
  {
    selector: `CallExpression[callee.name=/^(parseFloat|Number|parseInt)$/][arguments.0.name=/${MONEY_IDENT}/]`,
    message:
      'Prohibido convertir Money/Quantity a number (C11): usa los helpers BigInt de ~shared/money.',
  },
  {
    selector: `CallExpression[callee.name=/^(parseFloat|Number|parseInt)$/][arguments.0.property.name=/${MONEY_IDENT}/]`,
    message:
      'Prohibido convertir Money/Quantity a number (C11): usa los helpers BigInt de ~shared/money.',
  },
  {
    selector: `CallExpression[callee.object.name="Number"][callee.property.name=/^(parseFloat|parseInt)$/][arguments.0.name=/${MONEY_IDENT}/]`,
    message:
      'Prohibido convertir Money/Quantity a number (C11): usa los helpers BigInt de ~shared/money.',
  },
  {
    selector: `CallExpression[callee.object.name="Number"][callee.property.name=/^(parseFloat|parseInt)$/][arguments.0.property.name=/${MONEY_IDENT}/]`,
    message:
      'Prohibido convertir Money/Quantity a number (C11): usa los helpers BigInt de ~shared/money.',
  },
  {
    selector: `UnaryExpression[operator="+"][argument.name=/${MONEY_IDENT}/]`,
    message:
      'Prohibido el unario + sobre Money/Quantity (C11): usa los helpers BigInt de ~shared/money.',
  },
  {
    selector: `UnaryExpression[operator="+"][argument.property.name=/${MONEY_IDENT}/]`,
    message:
      'Prohibido el unario + sobre Money/Quantity (C11): usa los helpers BigInt de ~shared/money.',
  },
]

export default withNuxt(
  {
    // Prettier es el dueño del formato (FAD §23.4) y auto-cierra los elementos
    // void (<input />): se alinea la regla de Vue para que no peleen entre sí.
    name: 'imperio/prettier-compat',
    files: ['**/*.vue'],
    rules: {
      'vue/html-self-closing': [
        'warn',
        { html: { void: 'always', normal: 'always', component: 'always' } },
      ],
    },
  },
  {
    // Regla global anti-float sobre dinero/cantidades (C11).
    name: 'imperio/money-no-float',
    rules: {
      'no-restricted-syntax': ['error', ...moneyFloatSelectors],
    },
  },
  {
    // Fronteras de capa (FAD §8/§10.3): shared/ y domain/ son dominio puro,
    // framework-agnostic. No importan de app/, Nuxt, Vue ni Pinia.
    name: 'imperio/kernel-boundaries',
    files: ['shared/**/*.{ts,tsx}', 'domain/**/*.{ts,tsx}'],
    rules: {
      'no-restricted-imports': [
        'error',
        {
          patterns: [
            {
              group: [
                'vue',
                'vue/*',
                'vue-router',
                'vue-router/*',
                '@vue/*',
                'nuxt',
                'nuxt/*',
                '#app',
                '#app/*',
                '#imports',
                '#components',
                'pinia',
                '@pinia/*',
              ],
              message:
                'shared/ y domain/ son kernel framework-agnostic (FAD §9.2/§10.3): prohibido importar Vue/Nuxt/Pinia.',
            },
            {
              group: ['~/**', '~~/**', '@/**', '@@/**', '**/app/**', '../app/**', '../../app/**'],
              message:
                'shared/ y domain/ no dependen de la capa app/ (dirección de dependencias del FAD §8).',
            },
          ],
        },
      ],
    },
  },
  {
    // network/ es infraestructura: puede usar tipos del contrato y del kernel,
    // pero nunca la capa de presentación app/ (los DTO no salen de aquí, O5).
    name: 'imperio/network-boundaries',
    files: ['network/**/*.{ts,tsx}'],
    rules: {
      'no-restricted-imports': [
        'error',
        {
          patterns: [
            {
              group: ['~/**', '~~/**', '@/**', '@@/**', '**/app/**', '../app/**', '../../app/**'],
              message:
                'network/ no importa de app/ (los DTO crudos no salen de la capa de red, FAD O5).',
            },
          ],
        },
      ],
    },
  },
  {
    // Los tipos generados del contrato no se lintan (no se editan a mano, ADR-021).
    ignores: ['types/api.d.ts'],
  },
)

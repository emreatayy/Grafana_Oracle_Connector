// PromQL monarch language for the query editor (report §14.1, F7).
//
// Built directly on monaco's monarch machinery (same shape the standard SQL
// language from @grafana/plugin-ui uses) with PromQL-specific tokens: `#` line
// comments, `'…"` and backtick strings, durations (5m, 1h30m), label matchers
// inside `{...}`, aggregation keywords and the range/aggregation functions.
// `$__…` macros and `$var` template variables are highlighted the same way as
// the SQL editor so dashboard variables read naturally here too.
import { type LanguageDefinition, type SQLMonarchLanguage } from '@grafana/plugin-ui';
import type { languages } from 'monaco-editor';

const PROMQL_KEYWORDS = [
  // aggregation / grouping
  'by',
  'without',
  'on',
  'ignoring',
  'group_left',
  'group_right',
  'offset',
  // binary-set logic
  'and',
  'or',
  'unless',
  'bool',
  // literals
  'inf',
  'nan',
  'true',
  'false',
];

const PROMQL_FUNCTIONS = [
  // aggregations over a vector
  'sum',
  'avg',
  'count',
  'min',
  'max',
  'group',
  'stddev',
  'stdvar',
  'topk',
  'bottomk',
  'count_values',
  'quantile',
  'limitk',
  // rate / derivative
  'rate',
  'irate',
  'increase',
  'delta',
  'deriv',
  'resets',
  'predict_linear',
  // ranges
  'avg_over_time',
  'min_over_time',
  'max_over_time',
  'sum_over_time',
  'count_over_time',
  'stddev_over_time',
  'stdvar_over_time',
  'quantile_over_time',
  'last_over_time',
  'present_over_time',
  'absent_over_time',
  // labels / series
  'absent',
  'label_replace',
  'label_join',
  'sort',
  'sort_desc',
  'histogram_quantile',
  // time / date
  'time',
  'timestamp',
  'vector',
  'scalar',
  'year',
  'month',
  'day_of_month',
  'day_of_week',
  'day_of_year',
  'days_in_month',
  'hour',
  'minute',
  'second',
  'date',
  // math
  'abs',
  'ceil',
  'clamp',
  'clamp_max',
  'clamp_min',
  'exp',
  'floor',
  'ln',
  'log2',
  'log10',
  'round',
  'sqrt',
  'sgn',
  'acos',
  'acosh',
  'asin',
  'asinh',
  'atan',
  'atanh',
  'cos',
  'cosh',
  'deg',
  'pi',
  'rad',
  'sin',
  'sinh',
  'tan',
  'tanh',
];

const PROMQL_OPERATORS = [
  // arithmetic
  '+',
  '-',
  '*',
  '/',
  '%',
  '^',
  // comparison
  '==',
  '!=',
  '>',
  '<',
  '>=',
  '<=',
  // label matchers
  '=',
  '=~',
  '!~',
];

const prometheusLanguage: SQLMonarchLanguage = {
  defaultToken: '',
  tokenPostfix: '.promql',
  ignoreCase: false, // PromQL keywords/functions are lowercase
  brackets: [
    { open: '{', close: '}', token: 'delimiter.curly' },
    { open: '[', close: ']', token: 'delimiter.square' },
    { open: '(', close: ')', token: 'delimiter.parenthesis' },
  ],
  keywords: PROMQL_KEYWORDS,
  builtinFunctions: PROMQL_FUNCTIONS,
  operators: PROMQL_OPERATORS,
  tokenizer: {
    root: [
      { include: '@templateVariables' },
      { include: '@macros' },
      { include: '@comments' },
      { include: '@whitespace' },
      // durations first so 5m/1h30m win over plain numbers (they share the
      // digit prefix with metrics like node_1m only when the suffix follows).
      [/\d*\.?\d+(ms|s|m|h|d|w|y)/, 'number'],
      { include: '@numbers' },
      { include: '@strings' },
      [/[{}\[\]()]/, '@brackets'],
      // metric / label names, keywords and functions
      [
        /[a-zA-Z_:][a-zA-Z0-9_:]*/,
        {
          cases: {
            '@builtinFunctions': 'predefined',
            '@keywords': 'keyword',
            '@default': 'identifier',
          },
        },
      ],
      [/[=!<>~]+/, 'operator'],
      [/[+*/^%|-]/, 'operator'],
    ],
    templateVariables: [[/\$[a-zA-Z0-9_]+/, 'variable']],
    macros: [[/\$__[a-zA-Z0-9_-]+/, 'type']],
    whitespace: [[/[ \t\r\n]+/, 'white']],
    comments: [[/#.*$/, 'comment']],
    numbers: [
      [/0[xX][0-9a-fA-F]+/, 'number'],
      [/((\d+(\.\d*)?)|(\.\d+))([eE][-+]?\d+)?/, 'number'],
    ],
    strings: [
      [/`/, { token: 'string', next: '@backtickString' }],
      [/'/, { token: 'string', next: '@singleString' }],
      [/"/, { token: 'string', next: '@doubleString' }],
    ],
    backtickString: [
      [/[^`]+/, 'string'],
      [/`/, { token: 'string', next: '@pop' }],
    ],
    singleString: [
      [/[^']+/, 'string'],
      [/''/, 'string'],
      [/'/, { token: 'string', next: '@pop' }],
    ],
    doubleString: [
      [/[^"]+/, 'string'],
      [/""/, 'string'],
      [/"/, { token: 'string', next: '@pop' }],
    ],
  },
};

const prometheusConf: languages.LanguageConfiguration = {
  comments: { lineComment: '#' },
  brackets: [
    ['{', '}'],
    ['[', ']'],
    ['(', ')'],
  ],
  autoClosingPairs: [
    { open: '{', close: '}' },
    { open: '[', close: ']' },
    { open: '(', close: ')' },
    { open: "'", close: "'" },
    { open: '"', close: '"' },
    { open: '`', close: '`' },
  ],
  surroundingPairs: [
    { open: '{', close: '}' },
    { open: '[', close: ']' },
    { open: '(', close: ')' },
    { open: "'", close: "'" },
    { open: '"', close: '"' },
    { open: '`', close: '`' },
  ],
};

export const promQLSQLLanguage: LanguageDefinition = {
  id: 'promql',
  loader: async () => ({ language: prometheusLanguage, conf: prometheusConf }),
};

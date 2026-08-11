// Oracle SQL monarch language for the query editor (report §14.1, F4).
//
// Based on the standard SQL language from @grafana/plugin-ui (itself the monaco
// basic SQL language) with Oracle-specific keywords, functions and operators
// layered on. The monarch tokenizer reads the `keywords` / `builtinFunctions` /
// `operators` arrays via its built-in @-cases, so overriding them is enough to
// change what gets highlighted. F9 adds a completionProvider (tables/columns
// from the metadata resource) on top of the same language definition — the
// plugin-ui SQLEditor feeds the provider's `tables`/`columns` resolvers into
// the SuggestionKind.Tables/Columns registry entries that are otherwise empty.
import {
  getStandardSQLCompletionProvider,
  grafanaStandardSQLLanguage,
  grafanaStandardSQLLanguageConf,
  type ColumnDefinition,
  type LanguageDefinition,
  type SQLMonarchLanguage,
  type TableDefinition,
  type TableIdentifier,
} from '@grafana/plugin-ui';

// The subset of the datasource the completion resolvers need (report §14.3).
// Threaded in by the QueryEditor so the language definition stays pure.
export interface OracleMetadataClient {
  getMetadataTables(prefix?: string): Promise<string[]>;
  getMetadataColumns(table: string): Promise<Array<{ name: string; dataType: string; comment?: string }>>;
}

// Oracle-specific SQL/PL/SQL words layered on the ANSI set.
const ORACLE_KEYWORDS = [
  // statements & clauses
  'CONNECT',
  'START',
  'FETCH',
  'OFFSET',
  'MINUS',
  'INTERSECT',
  'DUAL',
  'MATCH_RECOGNIZE',
  'MODEL',
  'MERGE',
  // schema objects & types
  'VARCHAR2',
  'NVARCHAR2',
  'CLOB',
  'BLOB',
  'NUMBER',
  'TIMESTAMP',
  'PLS_INTEGER',
  'SEQUENCE',
  'TRIGGER',
  'PACKAGE',
  'PROCEDURE',
  'FUNCTION',
  'PIPELINED',
  // PL/SQL control flow (F15 command editor)
  'BEGIN',
  'EXCEPTION',
  'WHEN',
  'ELSIF',
  'THEN',
  'END',
  'LOOP',
  'WHILE',
  'EXIT',
  'INTO',
  'RAISE',
  'AUTHID',
  'CURRENT_USER',
  // pseudocolumns
  'ROWNUM',
  'ROWID',
  'LEVEL',
  'PRIOR',
];

// Oracle built-in functions (numeric, string, date, aggregate, analytic).
const ORACLE_FUNCTIONS = [
  // numeric
  'ABS',
  'CEIL',
  'FLOOR',
  'MOD',
  'POWER',
  'ROUND',
  'SIGN',
  'SQRT',
  'EXP',
  'LN',
  'LOG',
  // string
  'SUBSTR',
  'INSTR',
  'LENGTH',
  'LENGTHB',
  'CONCAT',
  'LOWER',
  'UPPER',
  'INITCAP',
  'TRIM',
  'LTRIM',
  'RTRIM',
  'LPAD',
  'RPAD',
  'REPLACE',
  'TRANSLATE',
  'LISTAGG',
  'REGEXP_LIKE',
  'REGEXP_SUBSTR',
  'REGEXP_REPLACE',
  'REGEXP_INSTR',
  'REGEXP_COUNT',
  // null / conditionals
  'NVL',
  'NVL2',
  'DECODE',
  'COALESCE',
  'NULLIF',
  'GREATEST',
  'LEAST',
  // date/time
  'SYSDATE',
  'SYSTIMESTAMP',
  'CURRENT_DATE',
  'CURRENT_TIMESTAMP',
  'EXTRACT',
  'TO_DATE',
  'TO_TIMESTAMP',
  'TO_CHAR',
  'TO_NUMBER',
  'ADD_MONTHS',
  'MONTHS_BETWEEN',
  'NEXT_DAY',
  'LAST_DAY',
  'CAST',
  // aggregate
  'COUNT',
  'SUM',
  'AVG',
  'MIN',
  'MAX',
  'STDDEV',
  'VARIANCE',
  'MEDIAN',
  // analytic
  'RANK',
  'DENSE_RANK',
  'ROW_NUMBER',
  'LAG',
  'LEAD',
  'FIRST_VALUE',
  'LAST_VALUE',
];

const ORACLE_OPERATORS = ['||'];

// Reuse the standard monarch language, replacing the ANSI keyword/function/op
// lists with the Oracle ones and giving the tokens a distinct CSS postfix so
// they never collide with a host "sql" language.
const oracleLanguage: SQLMonarchLanguage = {
  ...grafanaStandardSQLLanguage,
  tokenPostfix: '.oracle',
  keywords: [...(grafanaStandardSQLLanguage.keywords ?? []), ...ORACLE_KEYWORDS],
  builtinFunctions: [...(grafanaStandardSQLLanguage.builtinFunctions ?? []), ...ORACLE_FUNCTIONS],
  operators: [...(grafanaStandardSQLLanguage.operators ?? []), ...ORACLE_OPERATORS],
  logicalOperators: [...(grafanaStandardSQLLanguage.logicalOperators ?? []), 'NOT'],
};

// buildOracleLanguage assembles the SQLEditor language for a datasource. The
// completionProvider is where plugin-ui's schema completion plugs in: it starts
// from the standard keyword/function/operator provider (so the Oracle monarch
// builtinFunctions keep working) and layers the metadata-driven table/column
// resolvers on top. `metadata` is captured at mount time and is stable for the
// datasource's lifetime, so late resolver calls are safe.
export function buildOracleLanguage(metadata: OracleMetadataClient): LanguageDefinition {
  return {
    id: 'oracle-sql',
    loader: async () => ({
      language: oracleLanguage,
      conf: grafanaStandardSQLLanguageConf,
    }),
    completionProvider: (monaco, language) => {
      const standard = getStandardSQLCompletionProvider(monaco, language ?? oracleLanguage);
      return {
        ...standard,
        // After FROM / after a schema — offer the accessible tables, narrowed
        // by the identifier being typed.
        tables: {
          resolve: async (identifier: TableIdentifier | null): Promise<TableDefinition[]> => {
            const prefix = identifier?.table ?? '';
            const tables = await metadata.getMetadataTables(prefix);
            // completion === name makes the SQLEditor advance the cursor after
            // insertion (its InsertAsSnippet rule for tables).
            return tables.map((name) => ({ name, completion: name }));
          },
        },
        // After SELECT/WHERE/ORDER BY with a known table — offer its columns.
        // dataType becomes the completion detail so DATE/TIMESTAMP columns stand
        // out as time-column candidates.
        columns: {
          resolve: async (identifier?: TableIdentifier): Promise<ColumnDefinition[]> => {
            if (!identifier?.table) {
              return [];
            }
            const qualified = identifier.schema ? `${identifier.schema}.${identifier.table}` : identifier.table;
            const cols = await metadata.getMetadataColumns(qualified);
            return cols.map((c) => ({ name: c.name, completion: c.name, type: c.dataType, description: c.comment }));
          },
        },
      };
    },
  };
}

-- Adds a folded column. This copy is the original text; the tokenizer still
-- strips accents from it. On startup the store rewrites the column with
-- ligatures and stroke letters reduced as well, so "coeur" matches "cœur".
-- Snippets are read from the original columns.

DROP TABLE documents_fts;

CREATE VIRTUAL TABLE documents_fts USING fts5(
    title,
    body,
    tags,
    type_name,
    folded,
    document_id UNINDEXED,
    tokenize = 'unicode61 remove_diacritics 2'
);

INSERT INTO documents_fts (title, body, tags, type_name, folded, document_id)
SELECT d.title,
       COALESCE(dc.body, ''),
       TRIM(
         COALESCE((SELECT group_concat(tg.name, ' ')
                     FROM document_tags dt JOIN tags tg ON tg.id = dt.tag_id
                    WHERE dt.document_id = d.id), '')
         || ' ' || COALESCE(cr.name, '')
       ),
       COALESCE(t.name, ''),
       d.title || ' ' || COALESCE(dc.body, '') || ' ' ||
         TRIM(
           COALESCE((SELECT group_concat(tg.name, ' ')
                       FROM document_tags dt JOIN tags tg ON tg.id = dt.tag_id
                      WHERE dt.document_id = d.id), '')
           || ' ' || COALESCE(cr.name, '')
         ) || ' ' || COALESCE(t.name, ''),
       d.id
  FROM documents d
  LEFT JOIN document_types t ON t.id = d.type_id
  LEFT JOIN correspondents cr ON cr.id = d.correspondent_id
  LEFT JOIN document_content dc ON dc.document_id = d.id;

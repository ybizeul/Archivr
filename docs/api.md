# API

All routes are under `/api/v1`, except `GET /healthz`. Bodies are JSON, except uploads (multipart). Times are UTC RFC 3339 strings. Dates are `YYYY-MM-DD`.

## Conventions

**Authentication.** Sign-in sets the HttpOnly cookie `archivr_session`. API clients can instead send `Authorization: Bearer <token>`, where the token comes from the login response.

**Roles.** Routes marked *admin* return `403 FORBIDDEN` to a user with the `user` role.

**Errors.** Every error has one shape:

```json
{ "error": { "code": "INVALID", "message": "title is required" } }
```

Codes in use:

| Status | Code | When |
| --- | --- | --- |
| 400 | `INVALID`, `INVALID_JSON`, `INVALID_ID`, `INVALID_SETTINGS`, `INVALID_MAPPING` | Bad input. `INVALID` also covers "cannot delete your own account" and "last admin". |
| 401 | `UNAUTHORIZED`, `INVALID_CREDENTIALS` | No session, or wrong email or password. Both sign-in failures use the same message. |
| 403 | `FORBIDDEN`, `LOCAL_LOGIN_DISABLED` | Admin route for a user, or local sign-in turned off. |
| 404 | `NOT_FOUND`, `FILE_MISSING`, `THUMBNAIL_UNAVAILABLE`, `SHARE_INVALID` | Unknown ID or path. The file is missing on disk, or the type has no preview. The share link is not valid. |
| 410 | `SHARE_EXPIRED` | The share link is past its expiry. |
| 409 | `DUPLICATE`, `CONFLICT`, `FILE_EXISTS`, `IN_USE`, `IMPORT_RUNNING` | Same bytes already filed; name already taken; a file with that name is already on disk; the item is still in use; or a Paperless import is already running. |
| 413 | `TOO_LARGE` | Upload over 100 MB. |
| 415 | `UNSUPPORTED_TYPE` | Upload is not a supported type. |
| 429 | `RATE_LIMITED` | Too many failed sign-ins. |
| 502 | `IMAP_FAILED`, `PAPERLESS_FAILED`, `OIDC_DISCOVERY_FAILED` | The mail, Paperless or identity server could not be reached or refused. |
| 500 | `INTERNAL`, `DB_ERROR` | Details are in the server log. |

Unknown paths under `/api/` return a JSON `404 NOT_FOUND`.

**Lists.** Collections return `{ "items": [...] }`. Document search also returns `total`.

## Health

`GET /healthz` returns `{ "status": "ok" }`. It is outside `/api/v1`, and the container uses it as its readiness check.

## Session

| Method and path | Body or query | Result |
| --- | --- | --- |
| `POST /auth/login` | `{ "email", "password" }` | `200` with `{ "user", "token" }` and sets the cookie. Failures: `401 INVALID_CREDENTIALS`, `403 LOCAL_LOGIN_DISABLED` when SSO is required, `429 RATE_LIMITED` after eight failures for one email and client address within 15 minutes. |
| `POST /auth/logout` | | Clears the cookie. `204`, or `200` with `{ "redirect" }` when SSO is on and the provider has an end-session endpoint. The browser then goes to that URL, which ends the provider session and returns to `/login?signed_out=1`. |
| `GET /auth/me` | | The signed-in user: `id`, `email`, `name`, `role`, `has_password`, `created_at`. |
| `GET /auth/oidc/status` | | `{ "enabled", "disable_local_login" }`. |
| `GET /auth/oidc/start` | | Redirects to the identity provider. `400 OIDC_DISABLED` if SSO is off. |
| `GET /auth/oidc/callback` | `state`, `code` from the provider | Redirects into the app with a session cookie. Failures redirect to `/login` with an error code. |

## Documents

| Method and path | Notes |
| --- | --- |
| `GET /inbox/count` | `{ "count", "folder_id" }`. `folder_id` is the INBOX folder (`1`). |
| `GET /documents` | Query: `q` (words, prefix-matched, ANDed), `folder`, `subtree=1` (include subfolders), `type`, `tag`, `from` and `to` (date bounds), `ocr` (`pending`, `done`, `failed`, `skipped`), `inbox=1`, `sort` (`relevance`, `newest`, `oldest`, `date`, `title`), `limit` (default 50), `offset`. Returns `{ "items", "total" }`. With `q`, items carry a `snippet` where matches are marked with control characters, not HTML. When `folder` is omitted, documents in Trash are left out. Pass the Trash folder id to list them. |
| `POST /documents` | Multipart, file in field `file`, optional `title`, optional `valid_until` (`YYYY-MM-DD`, the last valid day: see `PATCH`). The document goes into INBOX. `400 INVALID` for a bad `valid_until`. `201` with the document. `409 DUPLICATE` if the same bytes are already filed, naming the existing number. `415` and `413` as above. |
| `GET /documents/{id}` | The document with its folder, type, tags, OCR status and text. |
| `PATCH /documents/{id}` | Any of `title`, `document_date` (`YYYY-MM-DD` or `null`), `type_id`, `type` (a type name, matched without regard to case; a name not yet in the type list is created, and an empty name clears the type; wins over `type_id`), `correspondent` (name up to 200 characters, empty clears it; a name not yet in the correspondent list is added to it), `tags` (array of names), `folder_id`, `rotation` (`0`, `90`, `180` or `270` degrees clockwise: how the browser turns the document when it shows it; the file is never changed), `valid_until` (`YYYY-MM-DD`, the last day the document is valid, empty clears it; the day after, Archivr moves the document into the Trash folder. Moving an expired document out of Trash clears its date unless the same request sets one). Moves or renames the file when the folder or title changes. `400 INVALID` for bad values. |
| `DELETE /documents/{id}` | `204`. **Permanent.** Removes the file, its sidecar, and the row. |
| `GET /documents/{id}/file` | The stored file, inline, with its stored content type and `X-Content-Type-Options: nosniff`. |
| `GET /documents/{id}/thumbnail` | JPEG preview, up to 480 px. |
| `GET /documents/{id}/suggestions` | `{ "dates": [{ "value", "label", "source" }], "titles": [] }`, found in the OCR text. Nothing is saved until a user applies one. |
| `POST /documents/{id}/reocr` | Queues OCR again. `202` with the document. |

## Share links

A share link opens one document for anyone who has it, without sign-in. It is a signed JWT (HS256) in the address. The token holds the document number and its own expiry, so nothing is stored on the server.

| Method and path | Notes |
| --- | --- |
| `POST /documents/{id}/share` | Needs a session. Body `{ "expires_in_seconds" }`, from 60 (1 minute) to 31536000 (365 days). `201` with `{ "url", "token", "expires_at" }`. `400 INVALID` for a value outside the range, `404` for an unknown document. |
| `GET /shares/{token}` | **No sign-in.** `{ "title", "document_date", "filename", "mime", "size", "rotation", "expires_at" }`. Folder, tags, correspondent and OCR text are not included. `410 SHARE_EXPIRED` after the expiry. `404 SHARE_INVALID` for a token that is not genuine, or whose document was deleted. |
| `GET /shares/{token}/file` | **No sign-in.** The file, inline. Add `?download=1` to get it as an attachment. Same errors. |

The link grants nothing else: the token is not a session, and the share and session signing keys differ. Both come from `JWT_SECRET`, so a link survives a restart exactly when sessions do, and changing `JWT_SECRET` ends every link. A link cannot be cancelled on its own. Deleting the document ends it. Share responses are sent with `Cache-Control: private, no-store` and `Referrer-Policy: no-referrer`.

The web app shows a link at `/share/{token}`.

## Folders

| Method and path | Body | Notes |
| --- | --- | --- |
| `GET /folders` | | `{ "items" }`, with `id`, `parent_id`, `name`, `path`, `is_system`, and counts of documents and subfolders. |
| `POST /folders` | `{ "name", "parent_id" }` | `201`. `parent_id` `null` or `0` puts the folder at the top level. |
| `PATCH /folders/{id}` | `{ "name", "parent_id" }` | Renames or moves the folder, its directory, and everything under it. `400` for INBOX and Trash. |
| `DELETE /folders/{id}` | | `204`. `409 IN_USE` while it holds documents or subfolders. `400` for INBOX and Trash. |

## Types and tags

| Method and path | Access | Notes |
| --- | --- | --- |
| `GET /types` | any user | `{ "items" }` with `id`, `name`, `color`, `document_count`. |
| `POST /types` | admin | `{ "name", "color" }`. `201`. Names are unique, case-insensitive. |
| `PATCH /types/{id}` | admin | `{ "name", "color" }`. |
| `DELETE /types/{id}` | admin | `409 IN_USE` while documents use it. |
| `GET /correspondents` | any user | `{ "items" }` with `id`, `name`, `document_count`, most used first. `?q=` filters by name. Correspondents are added by saving a document with a new name, not by a separate call. |
| `GET /tags` | any user | `{ "items" }` with `id`, `name`, `document_count`. `?q=` filters by name. |
| `PATCH /tags/{id}` | any user | `{ "name" }`. Renames the tag. A name already in use returns `409 CONFLICT`; tags are not merged. Search is refreshed. |
| `DELETE /tags/{id}` | any user | Removes the tag from every document. |

## Admin: settings

Secrets are never returned. A set secret comes back as `********`, and sending `********` back keeps the stored value.

| Method and path | Notes |
| --- | --- |
| `GET` / `PUT /admin/settings/auth` | `oidc_enabled`, `oidc_issuer`, `oidc_client_id`, `oidc_client_secret`, `oidc_admin_group`, `disable_local_login`. The GET also returns `callback_url` to register with the provider. `disable_local_login` requires SSO to be configured. |
| `GET` / `PUT /admin/settings/imap` | `enabled`, `imap_host`, `imap_port`, `imap_tls`, `imap_user`, `imap_password`, `imap_mailbox`, `poll_interval_seconds`. `400 INVALID_SETTINGS` for bad values. |
| `POST /admin/settings/imap/test` | Connects and signs in using the saved settings. `{ "ok", "mailbox" }` on success, `502 IMAP_FAILED` with the server's reason otherwise. 30-second limit. |
| `GET /admin/settings/imap/mailboxes` | Mailbox names on the server. |
| `GET` / `PUT /admin/settings/ocr` | `enabled`, `languages` (Tesseract codes joined by `+`, such as `eng+fra`), `timeout_seconds`. The GET also reports which engines are found and which languages are installed. |

## Admin: Paperless import

Reads from a Paperless-ngx server and files every document. See the README for what is carried over.

| Method and path | Notes |
| --- | --- |
| `GET` / `PUT /admin/settings/paperless` | `url`, `token` and `insecure_tls` (skip certificate checks, off by default). The token is returned as `********` with `token_set`, and sending `********` back keeps it. A trailing `/` or `/api` on the address is removed. `400 INVALID_SETTINGS` for an address that is not `http://` or `https://`. |
| `POST /admin/settings/paperless/test` | Connects with the saved settings. `{ "ok", "documents" }` with the number of documents the token can read, or `502 PAPERLESS_FAILED` with the reason. |
| `GET /admin/paperless/tags` | `{ "items" }` of the tags on the Paperless server, each with `name` and `document_count`. Uses the saved connection. `502 PAPERLESS_FAILED` when it cannot be read. |
| `GET` / `PUT /admin/paperless/mapping` | `{ "rules": [{ "tag", "folder_id" }] }`. The order is the priority: a document is filed by the first rule whose Paperless tag it carries, matched without regard to case. `PUT` replaces the list. `400 INVALID_MAPPING` for a missing tag or folder, or the same tag twice. `400 INVALID` when a folder does not exist. |
| `POST /admin/paperless/import` | Optional body `{ "folder_id" }`. The correspondent is saved in the document's `correspondent` field. `folder_id` `0` or absent is the folder for documents that match no rule, and means INBOX. The saved mapping is applied to new documents and to ones already in the archive: an existing document that matches a rule is moved to that folder, and one that matches none stays put. Starts the import and returns `202` with the progress. `409 IMPORT_RUNNING` while one is in progress, `400 INVALID_SETTINGS` when nothing is saved, `400 INVALID` for an unknown folder or a mapping that names a deleted folder. |
| `GET /admin/paperless/import` | Progress of the current or last run (kept in memory, so it is empty after a restart): `state` (`idle`, `running`, `done`, `failed`, `cancelled`), `total`, `processed`, `imported`, `already_imported`, `duplicates`, `moved` (existing documents a rule moved), `unsupported`, `failed`, `by_folder` (imported documents per folder path), `current`, `problems` (the latest 25, with reasons), `error`, `started_at`, `finished_at`. |
| `POST /admin/paperless/import/cancel` | Stops a running import. Documents already filed stay filed. Returns the progress. |

## Admin: users

| Method and path | Notes |
| --- | --- |
| `GET /admin/users` | `{ "items" }` with `id`, `email`, `name`, `role`, `has_password`, `created_at`. |
| `POST /admin/users` | `{ "email", "name", "role", "password" }`. The password is optional and, when given, at least 12 characters. Without one, the account signs in with SSO only. `201`. |
| `PATCH /admin/users/{id}` | `{ "name", "role" }`. `400 INVALID` when this would leave no admin, including demoting yourself as the last admin. |
| `DELETE /admin/users/{id}` | `400 INVALID` for your own account or the last admin. |
| `POST /admin/users/{id}/password` | `{ "password" }`, at least 12 characters. `404` for an unknown user. |

Sessions are signed tokens, checked against the user table on every request. Removing a user, or changing their role, takes effect on their next request.

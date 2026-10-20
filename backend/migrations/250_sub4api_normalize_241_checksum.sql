-- Sub4API 1.1.10/1.1.11 曾修改上游 241_add_typesafe_platform.sql（白名单加入 openai_bps），
-- 存量库记录的是改动后的 checksum。241 已恢复为上游原版，此处将历史 checksum
-- 归一为上游值，使库可在 Sub4API 与上游 Sub2API 之间互相切换。
--
-- 仅匹配已知的历史 checksum，可重入。

UPDATE schema_migrations
   SET checksum = 'b6559525bf8d0b5d7c617e7415944f0d1fae8c408e9131ace84139795f66dcd2'
 WHERE filename = '241_add_typesafe_platform.sql'
   AND checksum = '6034ab0415266a5299a64d49f56b52dab8c61a3c5b2eaa82327c81cc5698a871';

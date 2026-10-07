-- Demo end users for the dashboard's seeded dev box: written straight into the
-- project's tiffin_auth schema (test data only; nobody can sign in as them).
INSERT INTO tiffin_auth."user" (id, name, email, "emailVerified", "createdAt", "updatedAt", "twoFactorEnabled", banned, "banReason")
SELECT 'usr_' || substr(md5('tiffin-demo-user-' || i), 1, 20), n, lower(replace(n, ' ', '.')) || '@example.com', i % 5 <> 0,
       -- joined 5 to 13 days ago at uneven minutes, so every org owner joined before their org
       now() - ((120 + (i * 37) % 200) || ' hours')::interval - ((i * 17) % 59 || ' minutes')::interval,
       now() - ((i * 11) % 50 || ' hours')::interval - ((i * 29) % 57 || ' minutes')::interval,
       i % 4 = 0, i = 19, CASE WHEN i = 19 THEN 'Chargebacks on three orders' END
FROM unnest(ARRAY['Ada Lovelace','Grace Hopper','Alan Turing','Katherine Johnson','Margaret Hamilton','Radia Perlman','Barbara Liskov','Frances Allen','Hedy Lamarr','Annie Easley','Edsger Dijkstra','Donald Knuth','Shafi Goldwasser','Leslie Lamport','Sophie Wilson','Lynn Conway','Jean Bartik','Mary Keller','Carol Shaw','Evelyn Berezin','Charles Babbage','Tim Berners-Lee']) WITH ORDINALITY AS t(n, i)
ON CONFLICT DO NOTHING;
INSERT INTO tiffin_auth.account (id, "accountId", "providerId", "userId", "createdAt", "updatedAt")
SELECT 'acc_' || substr(md5('tiffin-demo-account-' || i), 1, 20), 'usr_' || substr(md5('tiffin-demo-user-' || i), 1, 20), CASE WHEN i % 3 = 0 THEN 'github' ELSE 'credential' END, 'usr_' || substr(md5('tiffin-demo-user-' || i), 1, 20), now(), now()
FROM generate_series(1, 22) i ON CONFLICT DO NOTHING;
-- Sessions start and were last used at different times, the way real ones are.
INSERT INTO tiffin_auth.session (id, "expiresAt", token, "createdAt", "updatedAt", "ipAddress", "userAgent", "userId")
SELECT 'ses_' || substr(md5('tiffin-demo-session-' || i || '-' || k), 1, 20), now() + interval '7 days', md5('demo' || i || k),
       now() - ((k * 3 + (i * 2) % 4) || ' hours')::interval, now() - (((i * 53) % 170) * k + 4 || ' minutes')::interval,
       (ARRAY['81.2.69.160','89.160.20.112','216.160.83.56'])[1 + k % 3],
       (ARRAY['Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15','Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1','Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36'])[1 + (i + k) % 3],
       'usr_' || substr(md5('tiffin-demo-user-' || i), 1, 20)
FROM generate_series(1, 22) i, generate_series(1, 2) k WHERE (i % 2 = 1 OR k = 1) AND i NOT IN (16, 19, 20) ON CONFLICT DO NOTHING;
INSERT INTO tiffin_auth.organization (id, name, slug, "createdAt") VALUES
  ('org_demo_1', 'Analytical Engines', 'analytical-engines', now() - interval '6 days'),
  ('org_demo_2', 'Compiler Club', 'compiler-club', now() - interval '3 days'),
  ('org_demo_3', 'Apollo Guidance', 'apollo-guidance', now() - interval '20 hours')
ON CONFLICT DO NOTHING;
INSERT INTO tiffin_auth.member (id, "organizationId", "userId", role, "createdAt") VALUES
  ('mem_demo_1', 'org_demo_1', 'usr_36eaf4aab6fcbb86a24a', 'owner', now() - interval '6 days'),
  ('mem_demo_2', 'org_demo_1', 'usr_37a3045e650d0f54b316', 'admin', now() - interval '5 days'),
  ('mem_demo_3', 'org_demo_1', 'usr_7f883d5bc730f839b6b0', 'member', now() - interval '4 days'),
  ('mem_demo_4', 'org_demo_1', 'usr_2b7d3cd95831a9cd797d', 'viewer', now() - interval '2 days'),
  ('mem_demo_5', 'org_demo_2', 'usr_1fc997b1ca7459c247f8', 'owner', now() - interval '3 days'),
  ('mem_demo_6', 'org_demo_2', 'usr_7b9b7a35c5dd914248b3', 'member', now() - interval '2 days'),
  ('mem_demo_7', 'org_demo_2', 'usr_b8d6a7233d4a1981371c', 'member', now() - interval '1 day'),
  ('mem_demo_8', 'org_demo_3', 'usr_009d9538c596c58c7c1b', 'owner', now() - interval '20 hours'),
  ('mem_demo_9', 'org_demo_3', 'usr_37ae59c630269809a45f', 'admin', now() - interval '18 hours')
ON CONFLICT DO NOTHING;
INSERT INTO tiffin_auth.invitation (id, "organizationId", email, role, status, "expiresAt", "createdAt", "inviterId") VALUES
  ('inv_demo_1', 'org_demo_1', 'luigi.menabrea@example.com', 'member', 'pending', now() + interval '5 days', now() - interval '2 days', 'usr_36eaf4aab6fcbb86a24a'),
  ('inv_demo_2', 'org_demo_3', 'don.eyles@example.com', 'member', 'pending', now() + interval '6 days', now() - interval '1 day', 'usr_009d9538c596c58c7c1b')
ON CONFLICT DO NOTHING;

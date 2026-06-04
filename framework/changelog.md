<<<<<<< HEAD
<<<<<<< HEAD
=======
- feat: file scheme (file://) support for pricing URL sync
- feat: paginated virtual key fetch to handle large numbers of keys
- fix: preserve non-pricing model pool entries across pricing reloads
>>>>>>> 3ffa9834e ([StepSecurity] Apply security best practices (#3697))
=======
- feat: `created_by` user attribution column for virtual keys (#3672)
- feat: `blacklisted_models` column for virtual key provider configs (#3653)
- fix: add monotonic `inc_number` log cursor so node usage reconciliation does
  not skip late async log writes (#3664)
- revert: `access_profile_id` direct access profile assignment on virtual keys
  (#3669)
- chore: drop the `access_profile_id` column from `governance_virtual_keys`
  (#3670)
>>>>>>> d754185a2 (highscale vkey flow improvements (#4007))
